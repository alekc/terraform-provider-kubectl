#!/bin/bash

function get_latest_version() {
    curl -s https://api.github.com/repos/hashicorp/terraform/git/refs/tags | \
        jq ".[] | .ref | split(\"/\") | .[2] | select(. | startswith(\"$1\"))" | \
            sort -V -r | head -1
}

echo terraform_versions="[$(get_latest_version v0.15), $(get_latest_version v1.2), $(get_latest_version v1.3), $(get_latest_version v1.4), $(get_latest_version v1.5)]" >> $GITHUB_OUTPUT
echo k8s_versions='["1.31.0", "1.30.3", "1.29.7", "1.28.0", "1.27.3", "1.26.6", "1.25.11"]' >> $GITHUB_OUTPUT