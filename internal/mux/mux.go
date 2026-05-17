package mux

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-mux/tf5to6server"
	"github.com/hashicorp/terraform-plugin-mux/tf6muxserver"

	"github.com/alekc/terraform-provider-kubectl/internal/framework"
	"github.com/alekc/terraform-provider-kubectl/kubernetes"
)

// MuxServer combines the existing SDK v2 provider (upgraded from protocol 5
// to 6) with the plugin-framework provider that hosts the ephemeral
// kubectl_manifest resource. Both halves share the SDK v2 provider's
// configured meta (a *kubernetes.KubeProvider) via the SDKv2Meta callback,
// so cluster auth and the dynamic client are configured once.
func MuxServer(ctx context.Context, version string) (tfprotov6.ProviderServer, error) {
	sdkProvider := kubernetes.Provider()

	upgraded, err := tf5to6server.UpgradeServer(ctx, sdkProvider.GRPCProvider)
	if err != nil {
		return nil, err
	}

	frameworkProvider := framework.New(version, sdkProvider.Meta)

	providers := []func() tfprotov6.ProviderServer{
		func() tfprotov6.ProviderServer { return upgraded },
		providerserver.NewProtocol6(frameworkProvider),
	}

	muxer, err := tf6muxserver.NewMuxServer(ctx, providers...)
	if err != nil {
		return nil, err
	}
	return muxer, nil
}
