# ECR_REGISTRY is set by msbx. docker's output is shown only on failure: on success it warns about storing
# the credentials unencrypted, but what it stores is the placeholder
out=$(printf %s "$ECR_TOKEN" | docker login -u AWS --password-stdin "$ECR_REGISTRY" 2>&1) ||
  { echo "$out" >&2; echo "warning: ECR login failed; continuing without ECR access" >&2; }
