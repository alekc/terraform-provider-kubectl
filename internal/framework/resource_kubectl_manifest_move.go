package framework

import (
	"context"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Cross-provider state move support: lets practitioners migrate existing
// gavinbunney/kubectl `kubectl_manifest` resources into this provider with a
// Terraform 1.8+ `moved {}` block, no `terraform state replace-provider`
// dance. See ADR-0001 and README "Migrating from gavinbunney/kubectl".
//
// The schema diff (work/standalone/terraform-provider-kubectl-123/data/
// schema-diff.md) established that gavinbunney's 20 kubectl_manifest
// attributes map 1:1 to this provider by name and type, both at
// SchemaVersion 1. So the move is a passthrough of those 20 (plus the
// implicit SDK v2 `id`) with the 4 alekc-only attributes taking their
// defaults.

var _ resource.ResourceWithMoveState = (*manifestResource)(nil)

const (
	// gavinbunneyManifestTypeName is the source resource type name we move
	// from. gavinbunney and this provider share the same type name, which is
	// exactly why a mux-server sibling type could not solve #123 and a real
	// framework MoveState handler is required.
	gavinbunneyManifestTypeName = "kubectl_manifest"

	// gavinbunneyProviderAddressSuffix matches the source provider address in
	// HOSTNAME/NAMESPACE/TYPE form. The hostname is intentionally ignored
	// (per framework guidance) so a move works regardless of which registry
	// mirror the source provider was pulled from.
	gavinbunneyProviderAddressSuffix = "gavinbunney/kubectl"

	// defaultFieldManager matches the SDK v2 field_manager default. A moved
	// resource that never set field_manager (gavinbunney has no such
	// attribute) adopts the same default it would get on a fresh apply.
	defaultFieldManager = "kubectl"
)

// gavinbunneyManifestSourceModel mirrors the persisted state shape of
// gavinbunney/kubectl's kubectl_manifest at SchemaVersion 1: the 20 shared
// attributes plus the implicit SDK v2 `id`. The 4 alekc-only attributes
// (upgrade_api_version, field_manager, wait_for, delete_cascade) are absent
// from gavinbunney state and are filled with defaults on the target side.
type gavinbunneyManifestSourceModel struct {
	ID                    types.String `tfsdk:"id"`
	UID                   types.String `tfsdk:"uid"`
	LiveUID               types.String `tfsdk:"live_uid"`
	YAMLInCluster         types.String `tfsdk:"yaml_incluster"`
	LiveManifestInCluster types.String `tfsdk:"live_manifest_incluster"`
	APIVersion            types.String `tfsdk:"api_version"`
	Kind                  types.String `tfsdk:"kind"`
	Name                  types.String `tfsdk:"name"`
	Namespace             types.String `tfsdk:"namespace"`
	OverrideNamespace     types.String `tfsdk:"override_namespace"`
	YAMLBody              types.String `tfsdk:"yaml_body"`
	YAMLBodyParsed        types.String `tfsdk:"yaml_body_parsed"`
	SensitiveFields       types.List   `tfsdk:"sensitive_fields"`
	ForceNew              types.Bool   `tfsdk:"force_new"`
	ServerSideApply       types.Bool   `tfsdk:"server_side_apply"`
	ForceConflicts        types.Bool   `tfsdk:"force_conflicts"`
	ApplyOnly             types.Bool   `tfsdk:"apply_only"`
	IgnoreFields          types.List   `tfsdk:"ignore_fields"`
	Wait                  types.Bool   `tfsdk:"wait"`
	WaitForRollout        types.Bool   `tfsdk:"wait_for_rollout"`
	ValidateSchema        types.Bool   `tfsdk:"validate_schema"`
}

// gavinbunneyManifestSourceSchema is the source schema handed to the
// framework so it can decode SourceRawState into a typed SourceState. Only
// the attribute types matter for decoding; the Optional/Computed flags are
// cosmetic here. The framework decodes with IgnoreUndefinedAttributes, so any
// attribute gavinbunney might add in a future release is silently dropped
// rather than breaking the move.
func gavinbunneyManifestSourceSchema() *schema.Schema {
	str := schema.StringAttribute{Optional: true, Computed: true}
	b := schema.BoolAttribute{Optional: true, Computed: true}
	strList := schema.ListAttribute{Optional: true, Computed: true, ElementType: types.StringType}
	return &schema.Schema{
		Version: 1,
		Attributes: map[string]schema.Attribute{
			"id":                      str,
			"uid":                     str,
			"live_uid":                str,
			"yaml_incluster":          str,
			"live_manifest_incluster": str,
			"api_version":             str,
			"kind":                    str,
			"name":                    str,
			"namespace":               str,
			"override_namespace":      str,
			"yaml_body":               str,
			"yaml_body_parsed":        str,
			"sensitive_fields":        strList,
			"force_new":               b,
			"server_side_apply":       b,
			"force_conflicts":         b,
			"apply_only":              b,
			"ignore_fields":           strList,
			"wait":                    b,
			"wait_for_rollout":        b,
			"validate_schema":         b,
		},
	}
}

// waitForObjectType is the attr.Type of a single wait_for block element,
// matching the ListNestedBlock declared in Schema(). Used to build a
// correctly-typed null list for resources moved from gavinbunney (which has
// no wait_for attribute).
func waitForObjectType() types.ObjectType {
	conditionObj := types.ObjectType{AttrTypes: map[string]attr.Type{
		"type":   types.StringType,
		"status": types.StringType,
	}}
	fieldObj := types.ObjectType{AttrTypes: map[string]attr.Type{
		"key":        types.StringType,
		"value":      types.StringType,
		"value_type": types.StringType,
	}}
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"condition": types.ListType{ElemType: conditionObj},
		"field":     types.ListType{ElemType: fieldObj},
	}}
}

// MoveState returns the ordered list of cross-provider state movers. Only one
// source is supported today: gavinbunney/kubectl's kubectl_manifest.
func (r *manifestResource) MoveState(_ context.Context) []resource.StateMover {
	return []resource.StateMover{
		{
			SourceSchema: gavinbunneyManifestSourceSchema(),
			StateMover:   moveFromGavinbunneyManifest,
		},
	}
}

// moveFromGavinbunneyManifest transforms a gavinbunney/kubectl kubectl_manifest
// state into this provider's state. It is deliberately cautious: if the source
// type name or provider address does not match, it returns an unmodified
// response so the framework treats this mover as skipped (per StateMover
// contract). Only once both match does it commit to producing state or an
// error.
func moveFromGavinbunneyManifest(ctx context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
	if req.SourceTypeName != gavinbunneyManifestTypeName {
		return
	}
	if !strings.HasSuffix(req.SourceProviderAddress, gavinbunneyProviderAddressSuffix) {
		return
	}

	// From here the request is unambiguously ours, so a failure must surface
	// as an error rather than a silent skip (a silent skip would make the
	// framework report "implementation not found", masking the real cause).
	if req.SourceState == nil {
		resp.Diagnostics.AddError(
			"Unable to move kubectl_manifest from gavinbunney/kubectl",
			"The source state could not be decoded against the expected gavinbunney schema "+
				"(schema version "+strconv.FormatInt(req.SourceSchemaVersion, 10)+"). This provider supports "+
				"moving from gavinbunney/kubectl kubectl_manifest at schema version 1. "+
				"Check the terraform-provider-kubectl debug log for the underlying decode error.",
		)
		return
	}

	var src gavinbunneyManifestSourceModel
	resp.Diagnostics.Append(req.SourceState.Get(ctx, &src)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Passthrough of the 20 shared attributes plus id. The 4 alekc-only
	// attributes take their schema defaults: upgrade_api_version=false
	// (matches gavinbunney's always-recreate-on-api_version behaviour being
	// the conservative default), field_manager="kubectl", and wait_for /
	// delete_cascade null (unset, no-op).
	target := manifestResourceModel{
		ID:                    src.ID,
		UID:                   src.UID,
		LiveUID:               src.LiveUID,
		YAMLInCluster:         src.YAMLInCluster,
		LiveManifestInCluster: src.LiveManifestInCluster,
		APIVersion:            src.APIVersion,
		Kind:                  src.Kind,
		Name:                  src.Name,
		Namespace:             src.Namespace,
		OverrideNamespace:     src.OverrideNamespace,
		YAMLBody:              src.YAMLBody,
		YAMLBodyParsed:        src.YAMLBodyParsed,
		SensitiveFields:       src.SensitiveFields,
		ForceNew:              src.ForceNew,
		ServerSideApply:       src.ServerSideApply,
		ForceConflicts:        src.ForceConflicts,
		ApplyOnly:             src.ApplyOnly,
		IgnoreFields:          src.IgnoreFields,
		Wait:                  src.Wait,
		WaitForRollout:        src.WaitForRollout,
		ValidateSchema:        src.ValidateSchema,

		UpgradeAPIVersion: types.BoolValue(false),
		FieldManager:      types.StringValue(defaultFieldManager),
		DeleteCascade:     types.StringNull(),
		WaitFor:           types.ListNull(waitForObjectType()),
	}

	resp.Diagnostics.Append(resp.TargetState.Set(ctx, &target)...)
}
