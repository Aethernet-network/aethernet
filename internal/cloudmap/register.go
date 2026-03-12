// Package cloudmap provides automatic AWS Cloud Map service instance
// registration for AetherNet nodes running on Amazon ECS Fargate.
//
// When AETHERNET_CLOUDMAP_SERVICE_ID is set, a Registrar registers the node's
// private IP (fetched from ECS container metadata, then EC2 IMDS, then local
// interfaces as a last resort) with the specified Cloud Map service on Start
// and deregisters on Stop. This enables other nodes to discover peers via DNS
// rather than static AETHERNET_PEER configuration.
//
// The package is a no-op when the environment variable is absent, so the same
// binary can run on ECS and non-ECS hosts without any code changes.
package cloudmap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
)

// Registrar manages AWS Cloud Map service instance registration for this node.
type Registrar struct {
	client     *servicediscovery.Client
	serviceID  string
	instanceID string // set to the node's private IP on first Start()
	p2pPort    string
	apiPort    string
}

// NewRegistrar creates a Registrar if AETHERNET_CLOUDMAP_SERVICE_ID is set,
// otherwise returns nil. p2pPort and apiPort are the numeric port strings
// (e.g. "8337", "8338") advertised as instance attributes.
//
// A nil *Registrar is safe to call Start() and Stop() on — both are no-ops.
func NewRegistrar(p2pPort, apiPort string) *Registrar {
	serviceID := os.Getenv("AETHERNET_CLOUDMAP_SERVICE_ID")
	if serviceID == "" {
		return nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Warn("cloudmap: failed to load AWS config; registration skipped", "err", err)
		return nil
	}

	return &Registrar{
		client:    servicediscovery.NewFromConfig(cfg),
		serviceID: serviceID,
		p2pPort:   p2pPort,
		apiPort:   apiPort,
	}
}

// Start registers this node's private IP as an instance in the Cloud Map
// service. It fetches the IP using a fallback chain: ECS container metadata
// (Fargate), EC2 IMDS, then local network interfaces. If the IP cannot be
// determined or the registration call fails, a warning is logged and the node
// continues without Cloud Map registration.
func (r *Registrar) Start() {
	if r == nil {
		return
	}

	ip, method := fetchPrivateIP()
	if ip == "" {
		slog.Warn("cloudmap: could not determine private IP; Cloud Map registration skipped")
		return
	}
	r.instanceID = ip
	slog.Info("cloudmap: detected private IP", "ip", ip, "method", method)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := r.client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  aws.String(r.serviceID),
		InstanceId: aws.String(ip),
		Attributes: map[string]string{
			"AWS_INSTANCE_IPV4": ip,
			"AWS_INSTANCE_PORT": r.p2pPort,
			"API_PORT":          r.apiPort,
		},
	})
	if err != nil {
		slog.Warn("cloudmap: RegisterInstance failed", "service_id", r.serviceID, "ip", ip, "err", err)
		return
	}
	slog.Info("cloudmap: registered", "service_id", r.serviceID, "instance_id", ip)
}

// Stop deregisters this node's instance from Cloud Map. It is safe to call
// even when Start was never called or registration failed.
func (r *Registrar) Stop() {
	if r == nil || r.instanceID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := r.client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
		ServiceId:  aws.String(r.serviceID),
		InstanceId: aws.String(r.instanceID),
	})
	if err != nil {
		slog.Warn("cloudmap: DeregisterInstance failed", "instance_id", r.instanceID, "err", err)
		return
	}
	slog.Info("cloudmap: deregistered", "instance_id", r.instanceID)
}

// fetchPrivateIP returns the node's private IPv4 address and a string
// describing the method used to obtain it.
//
// Fallback chain:
//  1. ECS container metadata V4 endpoint (set automatically by Fargate)
//  2. EC2 Instance Metadata Service (IMDSv2 then IMDSv1)
//  3. First non-loopback IPv4 address from net.InterfaceAddrs()
func fetchPrivateIP() (ip, method string) {
	// 1. ECS container metadata V4 (Fargate).
	if ip = fetchECSPrivateIP(); ip != "" {
		return ip, "ecs-metadata-v4"
	}

	// 2. EC2 IMDS (IMDSv2 first, then IMDSv1).
	if ip = fetchIMDSPrivateIP(); ip != "" {
		return ip, "ec2-imds"
	}

	// 3. Local network interfaces (development / bare-metal fallback).
	if ip = fetchLocalIP(); ip != "" {
		return ip, "net.InterfaceAddrs"
	}

	return "", ""
}

// fetchECSPrivateIP reads the private IP from the ECS task metadata V4
// endpoint. Fargate sets ECS_CONTAINER_METADATA_URI_V4 automatically; the
// endpoint is not reachable on non-Fargate hosts.
func fetchECSPrivateIP() string {
	metaURI := os.Getenv("ECS_CONTAINER_METADATA_URI_V4")
	if metaURI == "" {
		return ""
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(metaURI + "/task")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return ""
	}

	// Parse the minimal structure we care about.
	var meta struct {
		Containers []struct {
			Networks []struct {
				IPv4Addresses []string `json:"IPv4Addresses"`
			} `json:"Networks"`
		} `json:"Containers"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return ""
	}

	for _, c := range meta.Containers {
		for _, n := range c.Networks {
			for _, addr := range n.IPv4Addresses {
				if addr != "" {
					return addr
				}
			}
		}
	}
	return ""
}

// fetchIMDSPrivateIP queries the EC2 Instance Metadata Service for the node's
// private IPv4 address. Tries IMDSv2 (token-based) first; falls back to
// IMDSv1 if the token request fails.
func fetchIMDSPrivateIP() string {
	const metaURL = "http://169.254.169.254/latest/meta-data/local-ipv4"
	const tokenURL = "http://169.254.169.254/latest/api/token"

	// IMDSv2: request a session token first.
	token, err := imdsRequest(http.MethodPut, tokenURL,
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "21600"})
	if err == nil && token != "" {
		ip, err := imdsRequest(http.MethodGet, metaURL,
			map[string]string{"X-aws-ec2-metadata-token": token})
		if err == nil {
			return ip
		}
	}

	// IMDSv1 fallback.
	ip, _ := imdsRequest(http.MethodGet, metaURL, nil)
	return ip
}

// fetchLocalIP returns the first non-loopback, non-link-local IPv4 address
// found on the local network interfaces. Used as a last-resort fallback.
func fetchLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}

// imdsRequest makes a single HTTP request to the EC2 metadata service and
// returns the trimmed body text. Returns an error on network failure, non-2xx
// status, or an empty body.
func imdsRequest(method, url string, headers map[string]string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("imds: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "", fmt.Errorf("imds: empty response")
	}
	return s, nil
}
