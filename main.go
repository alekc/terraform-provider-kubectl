package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/alekc/terraform-provider-kubectl/internal/framework"
)

const (
	providerAddr = "registry.terraform.io/alekc/kubectl"
	version      = "dev"
)

func main() {
	debug := flag.Bool("debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: providerAddr,
		Debug:   *debug,
	}

	factory := func() provider.Provider { return framework.New(version) }
	if err := providerserver.Serve(context.Background(), factory, opts); err != nil {
		log.Fatalf("failed to serve provider: %v", err)
	}
}
