package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"

	"github.com/alekc/terraform-provider-kubectl/internal/mux"
)

const (
	providerAddr = "registry.terraform.io/alekc/kubectl"
	version      = "dev"
)

func main() {
	debug := flag.Bool("debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	ctx := context.Background()
	muxer, err := mux.MuxServer(ctx, version)
	if err != nil {
		log.Fatalf("failed to build mux server: %v", err)
	}

	opts := []tf6server.ServeOpt{}
	if *debug {
		opts = append(opts, tf6server.WithManagedDebug())
	}

	if err := tf6server.Serve(providerAddr, func() tfprotov6.ProviderServer { return muxer }, opts...); err != nil {
		log.Fatalf("failed to serve provider: %v", err)
	}
}
