# Never put braces around the token variable: microsandbox expands the braced form from the host's
# environment, which holds the real token
git config --global url."https://x-access-token:$GH_TOKEN@github.com/".insteadOf "ssh://git@github.com/"
git config --global --add url."https://x-access-token:$GH_TOKEN@github.com/".insteadOf "git@github.com:"
