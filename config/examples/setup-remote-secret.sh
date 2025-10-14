#!/bin/bash

# Setup Remote Cluster Secret for CLS Controller
# This script helps create a kubeconfig secret for remote cluster access

set -e

# Default values
SECRET_NAME="remote-cluster-kubeconfig"
SECRET_KEY="kubeconfig"
NAMESPACE="cls-system"
KUBECONFIG_FILE=""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

usage() {
    echo "Usage: $0 --kubeconfig <path> [options]"
    echo ""
    echo "Options:"
    echo "  --kubeconfig <path>     Path to kubeconfig file for remote cluster (required)"
    echo "  --secret-name <name>    Name of secret to create (default: remote-cluster-kubeconfig)"
    echo "  --secret-key <key>      Key within secret for kubeconfig data (default: kubeconfig)"
    echo "  --namespace <ns>        Namespace to create secret in (default: cls-system)"
    echo "  --dry-run              Show commands without executing them"
    echo "  --help                 Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 --kubeconfig ./remote-cluster.yaml"
    echo "  $0 --kubeconfig ./prod-cluster.yaml --secret-name prod-cluster-kubeconfig"
    echo "  $0 --kubeconfig ./staging.yaml --namespace staging-system --dry-run"
}

# Parse command line arguments
DRY_RUN=false
while [[ $# -gt 0 ]]; do
    case $1 in
        --kubeconfig)
            KUBECONFIG_FILE="$2"
            shift 2
            ;;
        --secret-name)
            SECRET_NAME="$2"
            shift 2
            ;;
        --secret-key)
            SECRET_KEY="$2"
            shift 2
            ;;
        --namespace)
            NAMESPACE="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            usage
            exit 1
            ;;
    esac
done

# Validate required arguments
if [[ -z "$KUBECONFIG_FILE" ]]; then
    echo -e "${RED}Error: --kubeconfig is required${NC}"
    usage
    exit 1
fi

if [[ ! -f "$KUBECONFIG_FILE" ]]; then
    echo -e "${RED}Error: Kubeconfig file not found: $KUBECONFIG_FILE${NC}"
    exit 1
fi

echo -e "${BLUE}Setting up remote cluster secret...${NC}"
echo "  Kubeconfig file: $KUBECONFIG_FILE"
echo "  Secret name: $SECRET_NAME"
echo "  Secret key: $SECRET_KEY"
echo "  Namespace: $NAMESPACE"
echo ""

# Validate kubeconfig
echo -e "${BLUE}Validating kubeconfig...${NC}"
if ! kubectl --kubeconfig="$KUBECONFIG_FILE" cluster-info > /dev/null 2>&1; then
    echo -e "${YELLOW}Warning: Could not connect to remote cluster with provided kubeconfig${NC}"
    echo "This might be due to network connectivity or invalid credentials"
    read -p "Continue anyway? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborted"
        exit 1
    fi
else
    echo -e "${GREEN}✓ Kubeconfig is valid and can connect to cluster${NC}"
fi

# Check if namespace exists
echo -e "${BLUE}Checking target namespace...${NC}"
if ! kubectl get namespace "$NAMESPACE" > /dev/null 2>&1; then
    echo -e "${YELLOW}Namespace $NAMESPACE does not exist${NC}"

    if [[ "$DRY_RUN" == "true" ]]; then
        echo "Would run: kubectl create namespace $NAMESPACE"
    else
        read -p "Create namespace $NAMESPACE? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            kubectl create namespace "$NAMESPACE"
            echo -e "${GREEN}✓ Created namespace $NAMESPACE${NC}"
        else
            echo -e "${RED}Error: Target namespace $NAMESPACE does not exist${NC}"
            exit 1
        fi
    fi
else
    echo -e "${GREEN}✓ Namespace $NAMESPACE exists${NC}"
fi

# Check if secret already exists
if kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" > /dev/null 2>&1; then
    echo -e "${YELLOW}Secret $SECRET_NAME already exists in namespace $NAMESPACE${NC}"

    if [[ "$DRY_RUN" == "false" ]]; then
        read -p "Replace existing secret? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE"
            echo -e "${GREEN}✓ Deleted existing secret${NC}"
        else
            echo "Aborted"
            exit 1
        fi
    else
        echo "Would replace existing secret"
    fi
fi

# Create the secret
echo -e "${BLUE}Creating secret...${NC}"
CREATE_CMD="kubectl create secret generic $SECRET_NAME --from-file=$SECRET_KEY=$KUBECONFIG_FILE -n $NAMESPACE"

if [[ "$DRY_RUN" == "true" ]]; then
    echo "Would run: $CREATE_CMD"
else
    eval "$CREATE_CMD"
    echo -e "${GREEN}✓ Created secret $SECRET_NAME in namespace $NAMESPACE${NC}"
fi

# Verify the secret
if [[ "$DRY_RUN" == "false" ]]; then
    echo -e "${BLUE}Verifying secret...${NC}"

    # Check secret exists
    if kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" > /dev/null 2>&1; then
        echo -e "${GREEN}✓ Secret exists${NC}"

        # Check key exists
        if kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" -o jsonpath="{.data.$SECRET_KEY}" > /dev/null 2>&1; then
            echo -e "${GREEN}✓ Secret contains key '$SECRET_KEY'${NC}"

            # Verify kubeconfig is valid
            KUBECONFIG_DATA=$(kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" -o jsonpath="{.data.$SECRET_KEY}" | base64 -d)
            if echo "$KUBECONFIG_DATA" | kubectl --kubeconfig=/dev/stdin cluster-info > /dev/null 2>&1; then
                echo -e "${GREEN}✓ Kubeconfig in secret is valid${NC}"
            else
                echo -e "${YELLOW}Warning: Kubeconfig in secret cannot connect to cluster${NC}"
            fi
        else
            echo -e "${RED}✗ Secret does not contain key '$SECRET_KEY'${NC}"
        fi
    else
        echo -e "${RED}✗ Secret was not created successfully${NC}"
        exit 1
    fi
fi

echo ""
echo -e "${GREEN}🎉 Remote cluster secret setup complete!${NC}"
echo ""
echo "Next steps:"
echo "1. Create a ControllerConfig that references this secret:"
echo ""
echo "   apiVersion: cls.redhat.com/v1alpha1"
echo "   kind: ControllerConfig"
echo "   metadata:"
echo "     name: my-remote-controller"
echo "     namespace: $NAMESPACE"
echo "   spec:"
echo "     target:"
echo "       type: \"kube-api\""
echo "       kubeConfig:"
echo "         secretRef:"
echo "           name: \"$SECRET_NAME\""
echo "           key: \"$SECRET_KEY\""
echo ""
echo "2. See config/examples/remote-cluster.yaml for a complete example"
echo "3. Read docs/REMOTE_TARGETS.md for detailed documentation"
echo ""
echo -e "${BLUE}Secret details:${NC}"
echo "  Name: $SECRET_NAME"
echo "  Namespace: $NAMESPACE"
echo "  Key: $SECRET_KEY"