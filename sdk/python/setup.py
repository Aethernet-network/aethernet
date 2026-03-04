from setuptools import setup, find_packages

setup(
    name="aethernet",
    version="0.1.0",
    description="Python SDK for the AetherNet AI-agent financial protocol",
    long_description=(
        "AetherNet is a causal-DAG protocol for AI agent value exchange. "
        "This SDK provides a stdlib-only HTTP client plus optional tool "
        "integrations for LangChain, CrewAI, and the OpenAI Agents SDK."
    ),
    packages=find_packages(),
    python_requires=">=3.9",
    extras_require={
        "langchain": ["langchain-core>=0.1.0"],
        "crewai": ["crewai>=0.1.0"],
        "openai": ["openai-agents>=0.1.0"],
        "all": ["langchain-core>=0.1.0", "crewai>=0.1.0", "openai-agents>=0.1.0"],
    },
)
