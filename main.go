package main

import (
	"flag"

	"github.com/dfroberg/terraform-provider-kubectl/kubernetes"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
)

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := &plugin.ServeOpts{
		Debug:        debug,
		ProviderAddr: "registry.terraform.io/dfroberg/kubectl",
		ProviderFunc: func() *schema.Provider {
			return kubernetes.Provider()
		},
	}

	plugin.Serve(opts)
}
