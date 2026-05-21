package kubernetes

import (
	"fmt"
	"strings"
	"sync"

	hcl "github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/tryfunc"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/lang/funcs"
	ctyyaml "github.com/zclconf/go-cty-yaml"
	"github.com/zclconf/go-cty/cty"
	ctyconvert "github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// ParsePathTemplate evaluates the given HCL template body against the provided
// scalar variables and returns the rendered string. Used by the framework-side
// `kubectl_path_documents` data source to apply user-provided variable
// substitution to YAML files loaded from disk.
//
// Pure function with the cty stdlib + Terraform builtin function map (see
// pathDocumentTemplateFunctions below). Same function surface as upstream
// Terraform's own template-renderer, vendored at this provider's vendored
// version of `terraform/lang/funcs`.
func ParsePathTemplate(s string, vars map[string]string) (string, error) {
	expr, diags := hclsyntax.ParseTemplate([]byte(s), "<template_file>", hcl.Pos{Line: 1, Column: 1})
	if expr == nil || (diags != nil && diags.HasErrors()) {
		return "", diags
	}

	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{},
	}
	for k, v := range vars {
		ctx.Variables[k] = cty.StringVal(v)
	}
	ctx.Functions = pathDocumentTemplateFunctions()

	result, diags := expr.Value(ctx)
	if diags != nil && diags.HasErrors() {
		return "", diags
	}
	result, err := ctyconvert.Convert(result, cty.String)
	if err != nil {
		return "", fmt.Errorf("invalid template result: %s", err)
	}
	return result.AsString(), nil
}

// ValidatePathDocumentsVars returns a descriptive error if any of the
// supplied vars contain non-scalar values (lists or maps). The framework
// data source attaches this at its validators step; this helper exists so
// the rule is unit-testable without spinning up the framework wiring.
func ValidatePathDocumentsVars(attr string, vars map[string]any) error {
	var bad []string
	for k, v := range vars {
		switch v.(type) {
		case []any:
			bad = append(bad, fmt.Sprintf("%s (list)", k))
		case map[string]any:
			bad = append(bad, fmt.Sprintf("%s (map)", k))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%s: cannot contain non-primitives; bad keys: %s", attr, strings.Join(bad, ", "))
	}
	return nil
}

var (
	pathDocsFuncsLock                              = &sync.Mutex{}
	pathDocsFuncs     map[string]function.Function = nil
)

// pathDocumentTemplateFunctions returns the function map used to render HCL
// templates loaded by `kubectl_path_documents`. The function surface is taken
// verbatim from the vendored Terraform `lang/funcs` package; it's the same
// surface Terraform's own template renderer ships with, but pinned to
// whichever version this provider vendored, not to whatever the user is
// running.
func pathDocumentTemplateFunctions() map[string]function.Function {
	pathDocsFuncsLock.Lock()
	defer pathDocsFuncsLock.Unlock()
	if pathDocsFuncs != nil {
		return pathDocsFuncs
	}
	baseDir := "."
	pathDocsFuncs = map[string]function.Function{
		"abs":              stdlib.AbsoluteFunc,
		"abspath":          funcs.AbsPathFunc,
		"basename":         funcs.BasenameFunc,
		"base64decode":     funcs.Base64DecodeFunc,
		"base64encode":     funcs.Base64EncodeFunc,
		"base64gzip":       funcs.Base64GzipFunc,
		"base64sha256":     funcs.Base64Sha256Func,
		"base64sha512":     funcs.Base64Sha512Func,
		"bcrypt":           funcs.BcryptFunc,
		"can":              tryfunc.CanFunc,
		"ceil":             funcs.CeilFunc,
		"chomp":            funcs.ChompFunc,
		"cidrhost":         funcs.CidrHostFunc,
		"cidrnetmask":      funcs.CidrNetmaskFunc,
		"cidrsubnet":       funcs.CidrSubnetFunc,
		"cidrsubnets":      funcs.CidrSubnetsFunc,
		"coalesce":         funcs.CoalesceFunc,
		"coalescelist":     funcs.CoalesceListFunc,
		"compact":          funcs.CompactFunc,
		"concat":           stdlib.ConcatFunc,
		"contains":         funcs.ContainsFunc,
		"csvdecode":        stdlib.CSVDecodeFunc,
		"dirname":          funcs.DirnameFunc,
		"distinct":         funcs.DistinctFunc,
		"element":          funcs.ElementFunc,
		"chunklist":        funcs.ChunklistFunc,
		"file":             funcs.MakeFileFunc(baseDir, false),
		"fileexists":       funcs.MakeFileExistsFunc(baseDir),
		"fileset":          funcs.MakeFileSetFunc(baseDir),
		"filebase64":       funcs.MakeFileFunc(baseDir, true),
		"filebase64sha256": funcs.MakeFileBase64Sha256Func(baseDir),
		"filebase64sha512": funcs.MakeFileBase64Sha512Func(baseDir),
		"filemd5":          funcs.MakeFileMd5Func(baseDir),
		"filesha1":         funcs.MakeFileSha1Func(baseDir),
		"filesha256":       funcs.MakeFileSha256Func(baseDir),
		"filesha512":       funcs.MakeFileSha512Func(baseDir),
		"flatten":          funcs.FlattenFunc,
		"floor":            funcs.FloorFunc,
		"format":           stdlib.FormatFunc,
		"formatdate":       stdlib.FormatDateFunc,
		"formatlist":       stdlib.FormatListFunc,
		"indent":           funcs.IndentFunc,
		"index":            funcs.IndexFunc,
		"join":             funcs.JoinFunc,
		"jsondecode":       stdlib.JSONDecodeFunc,
		"jsonencode":       stdlib.JSONEncodeFunc,
		"keys":             funcs.KeysFunc,
		"length":           funcs.LengthFunc,
		"list":             funcs.ListFunc,
		"log":              funcs.LogFunc,
		"lookup":           funcs.LookupFunc,
		"lower":            stdlib.LowerFunc,
		"map":              funcs.MapFunc,
		"matchkeys":        funcs.MatchkeysFunc,
		"max":              stdlib.MaxFunc,
		"md5":              funcs.Md5Func,
		"merge":            funcs.MergeFunc,
		"min":              stdlib.MinFunc,
		"parseint":         funcs.ParseIntFunc,
		"pathexpand":       funcs.PathExpandFunc,
		"pow":              funcs.PowFunc,
		"range":            stdlib.RangeFunc,
		"regex":            stdlib.RegexFunc,
		"regexall":         stdlib.RegexAllFunc,
		"replace":          funcs.ReplaceFunc,
		"reverse":          funcs.ReverseFunc,
		"rsadecrypt":       funcs.RsaDecryptFunc,
		"setintersection":  stdlib.SetIntersectionFunc,
		"setproduct":       funcs.SetProductFunc,
		"setsubtract":      stdlib.SetSubtractFunc,
		"setunion":         stdlib.SetUnionFunc,
		"sha1":             funcs.Sha1Func,
		"sha256":           funcs.Sha256Func,
		"sha512":           funcs.Sha512Func,
		"signum":           funcs.SignumFunc,
		"slice":            funcs.SliceFunc,
		"sort":             funcs.SortFunc,
		"split":            funcs.SplitFunc,
		"strrev":           stdlib.ReverseFunc,
		"substr":           stdlib.SubstrFunc,
		"timestamp":        funcs.TimestampFunc,
		"timeadd":          funcs.TimeAddFunc,
		"title":            funcs.TitleFunc,
		"tostring":         funcs.MakeToFunc(cty.String),
		"tonumber":         funcs.MakeToFunc(cty.Number),
		"tobool":           funcs.MakeToFunc(cty.Bool),
		"toset":            funcs.MakeToFunc(cty.Set(cty.DynamicPseudoType)),
		"tolist":           funcs.MakeToFunc(cty.List(cty.DynamicPseudoType)),
		"tomap":            funcs.MakeToFunc(cty.Map(cty.DynamicPseudoType)),
		"transpose":        funcs.TransposeFunc,
		"trim":             funcs.TrimFunc,
		"trimprefix":       funcs.TrimPrefixFunc,
		"trimspace":        funcs.TrimSpaceFunc,
		"trimsuffix":       funcs.TrimSuffixFunc,
		"try":              tryfunc.TryFunc,
		"upper":            stdlib.UpperFunc,
		"urlencode":        funcs.URLEncodeFunc,
		"uuid":             funcs.UUIDFunc,
		"uuidv5":           funcs.UUIDV5Func,
		"values":           funcs.ValuesFunc,
		"yamldecode":       ctyyaml.YAMLDecodeFunc,
		"yamlencode":       ctyyaml.YAMLEncodeFunc,
		"zipmap":           funcs.ZipmapFunc,
	}
	pathDocsFuncs["templatefile"] = funcs.MakeTemplateFileFunc(baseDir, func() map[string]function.Function {
		return pathDocsFuncs
	})
	return pathDocsFuncs
}
