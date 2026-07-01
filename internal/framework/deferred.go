package framework

import "github.com/hashicorp/terraform-plugin-framework/datasource"

// deferIfConfigUnknown reports whether a data source read should be deferred
// and, when so, records the deferral on resp. A read is deferred when the
// Terraform client allows deferral (a deferral-aware workflow such as
// Terraform Stacks) and the data source configuration is not yet fully known,
// e.g. an input interpolated from a not-yet-applied resource. Callers must
// return immediately when this returns true. Gated on the client capability
// so the classic read path is unchanged. See #356.
func deferIfConfigUnknown(req datasource.ReadRequest, resp *datasource.ReadResponse) bool {
	if req.ClientCapabilities.DeferralAllowed && !req.Config.Raw.IsFullyKnown() {
		resp.Deferred = &datasource.Deferred{
			Reason: datasource.DeferredReasonDataSourceConfigUnknown,
		}
		return true
	}
	return false
}
