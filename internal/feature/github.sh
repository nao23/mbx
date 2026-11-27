# Each value replaces only itself, so running again (on every start) adds no duplicates
git config --global --replace-all url."https://x-access-token:$GH_TOKEN@github.com/".insteadOf "ssh://git@github.com/" '^ssh://git@github\.com/$'
git config --global --replace-all url."https://x-access-token:$GH_TOKEN@github.com/".insteadOf "git@github.com:" '^git@github\.com:$'
