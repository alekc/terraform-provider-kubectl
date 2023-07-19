#!/bin/bash

function get_latest_version() {
    curl -s https://api.github.com/repos/hashicorp/terraform/git/refs/tags | \
        jq ".[] | .ref | split(\"/\") | .[2] | select(. | startswith(\"$1\"))" | \
            sort -V -r | head -1
}

#echo terraform_versions="[$(get_latest_version v0.12), $(get_latest_version v0.13), $(get_latest_version v0.14), $(get_latest_version v0.15), $(get_latest_version v1.0), $(get_latest_version v1.1), $(get_latest_version v1.2), $(get_latest_version v1.3), $(get_latest_version v1.4), $(get_latest_version v1.5)]" >> $GITHUB_OUTPUT
#echo k8s_versions='["1.27.3", "1.26.6", "1.25.11", "1.20.15"]' >> $GITHUB_OUTPUT
echo terraform_versions="[$(get_latest_version v1.4), $(get_latest_version v1.5)]" >> $GITHUB_OUTPUT
echo k8s_versions='["1.29.0", "1.28.0", "1.27.3", "1.26.6", "1.25.11", "1.24.15"]' >> $GITHUB_OUTPUT
