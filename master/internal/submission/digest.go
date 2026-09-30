package submission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ghodss/yaml"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/determined-ai/determined/master/internal/templates"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/utilv1"
)

// The digest identifies the content of a client request, so it is taken before master defaults
// and merges: petnames, ports, keys, tokens, and pool defaults vary between identical requests.
// The idempotency key, dry run, and expected digest are not content, and neither are a file's
// mtime, uid, and gid. A named template is content: the digest binds its config as read at
// submit, so a template that changes between a plan and its launch fails the plan check. The
// master alone computes it, so clients treat it as opaque.

// TemplateReader returns the template of a name if the caller may view it, and nil if the template
// does not exist or the caller may not view it. Such a template digests as its name alone, which
// reveals nothing of a config the caller may not read, and the create then fails as it would
// without a digest.
type TemplateReader func(name string) (*model.Template, error)

// ViewableTemplates returns the TemplateReader of a user.
func ViewableTemplates(ctx context.Context, user *model.User) TemplateReader {
	return func(name string) (*model.Template, error) {
		tpl, err := templates.ViewableTemplate(ctx, name, user)
		if status.Code(err) == codes.NotFound {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		return &tpl, nil
	}
}

// templateFields adds the canonical config of a request's template to its fields and returns the
// template, or nil if it cannot be read. A nil readTemplate reads no template.
func templateFields(
	fields map[string]any, name string, readTemplate TemplateReader,
) (*model.Template, error) {
	var tpl *model.Template
	if readTemplate != nil {
		var err error
		if tpl, err = readTemplate(name); err != nil {
			return nil, fmt.Errorf("reading template %s: %w", name, err)
		}
	}
	var config any
	if tpl != nil {
		var err error
		if config, err = yamlConfig(string(tpl.Config)); err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", name, err)
		}
	}
	fields["template_config"] = config
	return tpl, nil
}

// digest returns the SHA-256 of the canonical JSON of fields, in lowercase hex.
func digest(fields map[string]any) (string, error) {
	b, err := canonicalJSON(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON encodes v as JSON with sorted object keys, UTF-8 text, and no HTML escaping.
// encoding/json sorts map keys, so v is built from maps rather than structs.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding the canonical request: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// structConfig returns a config sent as a protobuf Struct as plain JSON values.
func structConfig(config *structpb.Struct) any {
	if config == nil {
		return nil
	}
	return config.AsMap()
}

// yamlConfig returns a config sent as YAML text as plain JSON values. Numbers decode to
// float64, as they do from a Struct, so the two forms of one config canonicalize alike.
func yamlConfig(config string) (any, error) {
	if config == "" {
		return nil, nil
	}
	b, err := yaml.YAMLToJSON([]byte(config))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "parsing the config: %s", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "parsing the config: %s", err)
	}
	return v, nil
}

// manifest returns the files of a request as {path, type, mode, sha256} entries sorted by path.
func manifest(files []*utilv1.File) []any {
	entries := make([]map[string]any, 0, len(files))
	for _, f := range files {
		sum := sha256.Sum256(f.Content)
		entries = append(entries, map[string]any{
			"path":   f.Path,
			"type":   f.Type,
			"mode":   f.Mode,
			"sha256": hex.EncodeToString(sum[:]),
		})
	}
	key := func(e map[string]any) string {
		return fmt.Sprintf("%s\x00%d\x00%d\x00%s", e["path"], e["type"], e["mode"], e["sha256"])
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })

	out := make([]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	return out
}

func commandFields(
	workspaceID int32, templateName string, config *structpb.Struct, files []*utilv1.File,
) map[string]any {
	return map[string]any{
		"workspace_id":  workspaceID,
		"template_name": templateName,
		"config":        structConfig(config),
		"files":         manifest(files),
	}
}

func genericTaskFields(req *apiv1.CreateGenericTaskRequest) (map[string]any, error) {
	config, err := yamlConfig(req.Config)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"project_id":        req.ProjectId,
		"config":            config,
		"context_directory": manifest(req.ContextDirectory),
		"parent_id":         req.ParentId,
		"forked_from":       req.ForkedFrom,
		"inherit_context":   req.InheritContext,
		"no_pause":          req.NoPause,
	}, nil
}

func experimentFields(req *apiv1.CreateExperimentRequest) (map[string]any, error) {
	config, err := yamlConfig(req.Config)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"project_id":       req.ProjectId,
		"template":         req.Template,
		"config":           config,
		"model_definition": manifest(req.ModelDefinition),
		"parent_id":        req.ParentId,
		"activate":         req.Activate,
	}, nil
}

// requestDigest digests the content fields of a request together with its kind and admission.
func requestDigest(kind model.JobType, admission model.Admission, fields map[string]any) (string, error) {
	fields["kind"] = string(kind)
	fields["admission"] = string(admission)
	return digest(fields)
}
