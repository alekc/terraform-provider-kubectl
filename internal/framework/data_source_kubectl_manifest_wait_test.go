package framework

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// dataSourceWaitForObjectType mirrors the data source's wait_for
// block shape: condition + field nested lists. Used in tests to
// build a types.List value to feed into extractDataSourceWaitFor.
func dataSourceWaitForObjectType() types.ObjectType {
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

func TestExtractDataSourceWaitFor_NullOrUnknownReturnsNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	got, diags := extractWaitForBlock(ctx, types.ListNull(dataSourceWaitForObjectType()), "data source")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics on null: %v", diags)
	}
	if got != nil {
		t.Errorf("null list should yield nil WaitFor, got %+v", got)
	}

	got, diags = extractWaitForBlock(ctx, types.ListUnknown(dataSourceWaitForObjectType()), "data source")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics on unknown: %v", diags)
	}
	if got != nil {
		t.Errorf("unknown list should yield nil WaitFor, got %+v", got)
	}
}

func TestExtractDataSourceWaitFor_RejectsMultipleBlocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	objType := dataSourceWaitForObjectType()

	makeBlock := func(condType, condStatus string) attr.Value {
		condElem, _ := types.ObjectValue(map[string]attr.Type{
			"type":   types.StringType,
			"status": types.StringType,
		}, map[string]attr.Value{
			"type":   types.StringValue(condType),
			"status": types.StringValue(condStatus),
		})
		conds, _ := types.ListValue(types.ObjectType{AttrTypes: map[string]attr.Type{
			"type":   types.StringType,
			"status": types.StringType,
		}}, []attr.Value{condElem})
		obj, _ := types.ObjectValue(objType.AttrTypes, map[string]attr.Value{
			"condition": conds,
			"field":     types.ListNull(types.ObjectType{AttrTypes: map[string]attr.Type{"key": types.StringType, "value": types.StringType, "value_type": types.StringType}}),
		})
		return obj
	}

	list, _ := types.ListValue(objType, []attr.Value{
		makeBlock("Ready", "True"),
		makeBlock("Synced", "True"),
	})

	_, diags := extractWaitForBlock(ctx, list, "data source")
	if !diags.HasError() {
		t.Fatalf("expected error for multiple wait_for blocks; got no diagnostics")
	}
	found := false
	for _, e := range diags.Errors() {
		if e.Summary() == "too many wait_for blocks" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'too many wait_for blocks' diagnostic; got %v", diags)
	}
}

func TestExtractDataSourceWaitFor_PopulatesConditionsAndFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	objType := dataSourceWaitForObjectType()

	condElem, _ := types.ObjectValue(map[string]attr.Type{
		"type":   types.StringType,
		"status": types.StringType,
	}, map[string]attr.Value{
		"type":   types.StringValue("Ready"),
		"status": types.StringValue("True"),
	})
	conds, _ := types.ListValue(types.ObjectType{AttrTypes: map[string]attr.Type{
		"type":   types.StringType,
		"status": types.StringType,
	}}, []attr.Value{condElem})

	fieldElem, _ := types.ObjectValue(map[string]attr.Type{
		"key":        types.StringType,
		"value":      types.StringType,
		"value_type": types.StringType,
	}, map[string]attr.Value{
		"key":        types.StringValue("status.phase"),
		"value":      types.StringValue("Running"),
		"value_type": types.StringValue("eq"),
	})
	fields, _ := types.ListValue(types.ObjectType{AttrTypes: map[string]attr.Type{
		"key":        types.StringType,
		"value":      types.StringType,
		"value_type": types.StringType,
	}}, []attr.Value{fieldElem})

	blockObj, _ := types.ObjectValue(objType.AttrTypes, map[string]attr.Value{
		"condition": conds,
		"field":     fields,
	})
	list, _ := types.ListValue(objType, []attr.Value{blockObj})

	got, diags := extractWaitForBlock(ctx, list, "data source")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got == nil {
		t.Fatal("expected non-nil WaitFor")
	}
	if len(got.Condition) != 1 || got.Condition[0].Type != "Ready" || got.Condition[0].Status != "True" {
		t.Errorf("condition mismatch: %+v", got.Condition)
	}
	if len(got.Field) != 1 || got.Field[0].Key != "status.phase" || got.Field[0].Value != "Running" || got.Field[0].ValueType != "eq" {
		t.Errorf("field mismatch: %+v", got.Field)
	}
}
