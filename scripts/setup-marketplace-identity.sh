#!/usr/bin/env bash
# Provision the Azure identity that publishes the extension to the VS Code
# Marketplace, replacing the retiring global PAT (VSCE_PAT).
#
# Azure DevOps retires ALL global PATs on 2026-12-01. Organization-scoped PATs
# are not accepted by the Marketplace (microsoft/vscode#322741, still open), so
# the only durable path is Microsoft Entra ID: a user-assigned managed identity
# federated to this repository's GitHub Actions OIDC issuer, published with
# `vsce publish --azure-credential`.
#
# WHY A MANAGED IDENTITY AND NOT AN APP REGISTRATION
# An app registration is free and needs no subscription, so it looks like the
# obvious choice. It authenticates successfully and then fails at the publish
# call with `InvalidAccessException: The requested operation is not allowed`.
# Only a user-assigned managed identity works, and that is a real Azure
# resource, so it needs a subscription to live in (free tier is enough).
#
# WHAT THIS SCRIPT DOES NOT DO
# Two steps require an interactive browser session as you and cannot be
# scripted:
#   1. `az login` — run it yourself before this script.
#   2. Adding the identity as a member of the Marketplace publisher. There is
#      no public API for publisher membership; it is a web UI action. Run
#      `.github/workflows/marketplace-identity.yml` afterwards to obtain the
#      one ID that page will accept.
#
# Usage:
#   az login
#   bash scripts/setup-marketplace-identity.sh
#
# Override any default via environment variable, e.g.:
#   AZ_LOCATION=westus2 bash scripts/setup-marketplace-identity.sh

set -euo pipefail

AZ_RESOURCE_GROUP="${AZ_RESOURCE_GROUP:-nightgauge-marketplace}"
AZ_IDENTITY_NAME="${AZ_IDENTITY_NAME:-nightgauge-marketplace-publisher}"
AZ_LOCATION="${AZ_LOCATION:-eastus}"
GH_REPO="${GH_REPO:-nightgauge/nightgauge}"
# Must match the `environment:` of the publishing job in release.yml. Federated
# credentials scoped to a branch or tag authenticate the first release and then
# fail on every one after it; environment scoping is stable across tags.
GH_ENVIRONMENT="${GH_ENVIRONMENT:-production}"

info() { printf '\033[36m▶ %s\033[0m\n' "$*"; }
ok() { printf '\033[32m✓ %s\033[0m\n' "$*"; }
warn() { printf '\033[33m! %s\033[0m\n' "$*"; }
die() {
  printf '\033[31m✗ %s\033[0m\n' "$*" >&2
  exit 1
}

# ── 1. Preconditions ────────────────────────────────────────────────────────
command -v az >/dev/null 2>&1 ||
  die "Azure CLI not found. Install it (brew install azure-cli) and run 'az login'."

az account show >/dev/null 2>&1 ||
  die "Not signed in to Azure. Run 'az login' first."

ACCOUNT_JSON="$(az account show --output json)"
SUBSCRIPTION_ID="$(printf '%s' "$ACCOUNT_JSON" | jq -r '.id')"
SUBSCRIPTION_NAME="$(printf '%s' "$ACCOUNT_JSON" | jq -r '.name')"
TENANT_ID="$(printf '%s' "$ACCOUNT_JSON" | jq -r '.tenantId')"
SIGNED_IN_AS="$(printf '%s' "$ACCOUNT_JSON" | jq -r '.user.name')"
TENANT_NAME="$(printf '%s' "$ACCOUNT_JSON" | jq -r '.tenantDisplayName // "unknown"')"

info "Signed in as ......... $SIGNED_IN_AS"
info "Tenant ............... $TENANT_NAME ($TENANT_ID)"
info "Subscription ......... $SUBSCRIPTION_NAME ($SUBSCRIPTION_ID)"
echo

# ── 1b. Is this a real tenant, or a directory auto-created for an MSA? ──────
#
# The known dead end: a federated identity from a directory that merely wraps a
# personal Microsoft account, presented to a publisher owned by that same MSA,
# fails at publish with "corporate credentials required". Reported to
# vscode-vsce and closed as not planned.
#
# The tenant's DISPLAY NAME is not the tell. "Default Directory" is simply what
# Azure names any new tenant, including legitimate ones. The decisive signals
# are the signed-in user's UPN and the tenant's verified domains:
#
#   real tenant : upn=you@yourdomain.com          domains=[yourdomain.com, ...]
#   MSA wrapper : upn=you_yourdomain.com#EXT#@... domains=[...onmicrosoft.com]
#
# `#EXT#` marks an external (guest) identity — the account is not a member of
# the directory, so a token minted here does not represent the publisher's
# owner. Verified domains confirm it: a directory that has never had a custom
# domain verified only knows its own onmicrosoft.com.
UPN="$(az ad signed-in-user show --query userPrincipalName --output tsv 2>/dev/null || echo "")"
VERIFIED_DOMAINS="$(az rest --url https://graph.microsoft.com/v1.0/organization \
  --query 'value[0].verifiedDomains[].name' --output tsv 2>/dev/null | tr '\n' ' ' || echo "")"

info "Signed-in UPN ........ ${UPN:-<could not read>}"
info "Verified domains ..... ${VERIFIED_DOMAINS:-<could not read>}"
echo

case "$UPN" in
*'#EXT#'*)
  warn "This directory does not own your identity: the UPN carries '#EXT#',"
  warn "meaning you are a GUEST here, not a member. Azure created this"
  warn "directory when you signed up with a personal Microsoft account."
  warn ""
  warn "A managed identity created here would present a token that does not"
  warn "represent the owner of the 'nightgauge' publisher, and the publish"
  warn "will fail with 'corporate credentials required'. There is no"
  warn "workaround (closed upstream as not planned)."
  warn ""
  warn "To unblock, the publisher and the identity must live in the same real"
  warn "tenant: verify your own custom domain on an Entra tenant,"
  warn "create a MEMBER account there, and make that account an owner of the"
  warn "publisher. Until then, publish with a global PAT (works until"
  warn "2026-12-01, when Azure DevOps decommissions all of them)."
  echo
  die "Refusing to provision an identity that cannot publish. Re-run once the UPN is a member of a tenant with a verified custom domain."
  ;;
esac

ok "Identity is a member of this directory; federated publishing is viable."
echo

# ── 2. Resource group ───────────────────────────────────────────────────────
if az group show --name "$AZ_RESOURCE_GROUP" >/dev/null 2>&1; then
  ok "Resource group '$AZ_RESOURCE_GROUP' already exists"
else
  info "Creating resource group '$AZ_RESOURCE_GROUP' in $AZ_LOCATION..."
  az group create --name "$AZ_RESOURCE_GROUP" --location "$AZ_LOCATION" --output none
  ok "Created resource group '$AZ_RESOURCE_GROUP'"
fi

# ── 3. User-assigned managed identity ───────────────────────────────────────
if az identity show --name "$AZ_IDENTITY_NAME" --resource-group "$AZ_RESOURCE_GROUP" >/dev/null 2>&1; then
  ok "Managed identity '$AZ_IDENTITY_NAME' already exists"
else
  info "Creating user-assigned managed identity '$AZ_IDENTITY_NAME'..."
  az identity create \
    --name "$AZ_IDENTITY_NAME" \
    --resource-group "$AZ_RESOURCE_GROUP" \
    --location "$AZ_LOCATION" \
    --output none
  ok "Created managed identity '$AZ_IDENTITY_NAME'"
fi

IDENTITY_JSON="$(az identity show --name "$AZ_IDENTITY_NAME" --resource-group "$AZ_RESOURCE_GROUP" --output json)"
CLIENT_ID="$(printf '%s' "$IDENTITY_JSON" | jq -r '.clientId')"
PRINCIPAL_ID="$(printf '%s' "$IDENTITY_JSON" | jq -r '.principalId')"

# ── 4. Federated credential for GitHub Actions OIDC ─────────────────────────
FED_NAME="github-${GH_ENVIRONMENT}"
FED_SUBJECT="repo:${GH_REPO}:environment:${GH_ENVIRONMENT}"

if az identity federated-credential show \
  --name "$FED_NAME" \
  --identity-name "$AZ_IDENTITY_NAME" \
  --resource-group "$AZ_RESOURCE_GROUP" >/dev/null 2>&1; then
  EXISTING_SUBJECT="$(az identity federated-credential show \
    --name "$FED_NAME" \
    --identity-name "$AZ_IDENTITY_NAME" \
    --resource-group "$AZ_RESOURCE_GROUP" \
    --query subject --output tsv)"
  if [ "$EXISTING_SUBJECT" = "$FED_SUBJECT" ]; then
    ok "Federated credential '$FED_NAME' already matches $FED_SUBJECT"
  else
    die "Federated credential '$FED_NAME' exists but its subject is '$EXISTING_SUBJECT', not '$FED_SUBJECT'. Delete it and re-run."
  fi
else
  info "Creating federated credential '$FED_NAME' for $FED_SUBJECT..."
  az identity federated-credential create \
    --name "$FED_NAME" \
    --identity-name "$AZ_IDENTITY_NAME" \
    --resource-group "$AZ_RESOURCE_GROUP" \
    --issuer "https://token.actions.githubusercontent.com" \
    --subject "$FED_SUBJECT" \
    --audiences "api://AzureADTokenExchange" \
    --output none
  ok "Created federated credential '$FED_NAME'"
fi

# ── 5. Report ───────────────────────────────────────────────────────────────
echo
ok "Azure side complete."
echo
printf '  AZURE_CLIENT_ID  %s\n' "$CLIENT_ID"
printf '  AZURE_TENANT_ID  %s\n' "$TENANT_ID"
printf '  principalId      %s  (Entra object ID — NOT what the Marketplace wants)\n' "$PRINCIPAL_ID"
echo
cat <<EOF
Next steps:

  1. Store the two IDs as repository secrets:

       gh secret set AZURE_CLIENT_ID --repo $GH_REPO --body $CLIENT_ID
       gh secret set AZURE_TENANT_ID --repo $GH_REPO --body $TENANT_ID

  2. Get the Azure DevOps profile ID for this identity. Azure DevOps keeps its
     own identity record, distinct from both the ARM resource ID and the Entra
     object ID above, and the Marketplace accepts ONLY that third ID. It must
     be read while authenticated AS the identity, which only CI can do:

       gh workflow run marketplace-identity.yml --repo $GH_REPO

     The run prints the profile ID and doubles as an end-to-end check that the
     federated credential works.

  3. Add that profile ID as a member of the publisher, with the Contributor
     role:

       https://marketplace.visualstudio.com/manage/publishers/nightgauge

  4. Set the publish gate and tag a release:

       gh variable set MARKETPLACE_PUBLISH --body true --repo $GH_REPO
EOF
