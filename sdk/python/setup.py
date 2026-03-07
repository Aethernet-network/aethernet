from setuptools import setup, find_packages

with open("README.md", "r", encoding="utf-8") as f:
    long_description = f.read()

setup(
    name="aethernet-sdk",
    version="0.2.0",
    author="AetherNet",
    description="Python SDK for AetherNet — financial infrastructure for AI agents",
    long_description=long_description,
    long_description_content_type="text/markdown",
    url="https://github.com/Aethernet-network/aethernet",
    packages=find_packages(),
    python_requires=">=3.8",
    install_requires=["requests>=2.20.0"],
    extras_require={
        "crypto": ["cryptography>=41.0.0"],
        "langchain": ["langchain-core>=0.1.0"],
        "crewai": ["crewai>=0.1.0"],
        "openai": ["openai-agents>=0.1.0"],
        "all": [
            "cryptography>=41.0.0",
            "langchain-core>=0.1.0",
            "crewai>=0.1.0",
            "openai-agents>=0.1.0",
        ],
    },
    classifiers=[
        "Development Status :: 3 - Alpha",
        "Intended Audience :: Developers",
        "License :: OSI Approved :: MIT License",
        "Programming Language :: Python :: 3",
        "Programming Language :: Python :: 3.8",
        "Programming Language :: Python :: 3.9",
        "Programming Language :: Python :: 3.10",
        "Programming Language :: Python :: 3.11",
        "Programming Language :: Python :: 3.12",
        "Topic :: Software Development :: Libraries",
    ],
    keywords="ai agents payments settlement blockchain",
)
