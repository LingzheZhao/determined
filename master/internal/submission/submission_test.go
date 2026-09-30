package submission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/utilv1"
)

func TestValidateKey(t *testing.T) {
	for _, key := range []string{
		"a",
		"0f1c2d3e-4b5a-6978-8a9b-0c1d2e3f4a5b",
		"A.z_0:9-x",
		strings.Repeat("k", 128),
	} {
		require.NoError(t, ValidateKey(key), key)
	}
	for _, key := range []string{
		"",
		strings.Repeat("k", 129),
		"has space",
		"slash/key",
		"key\n",
		"ключ",
	} {
		err := ValidateKey(key)
		require.Equal(t, codes.InvalidArgument, status.Code(err), key)
	}
}

func TestCanonicalJSON(t *testing.T) {
	b, err := canonicalJSON(map[string]any{
		"b": 1.0,
		"a": map[string]any{"z": "<&>", "y": []any{2.5, nil, true}},
		"c": "ü",
	})
	require.NoError(t, err)
	require.Equal(t, `{"a":{"y":[2.5,null,true],"z":"<&>"},"b":1,"c":"ü"}`, string(b))
}

func commandRequest(t *testing.T) *apiv1.LaunchCommandRequest {
	config, err := structpb.NewStruct(map[string]any{
		"entrypoint": []any{"python", "train.py", "--lr=<1e-3>"},
		"resources":  map[string]any{"slots": 1, "resource_pool": "gpu"},
	})
	require.NoError(t, err)
	return &apiv1.LaunchCommandRequest{
		Config:      config,
		WorkspaceId: 2,
		Files: []*utilv1.File{
			{Path: "train.py", Type: 48, Content: []byte("print(1)"), Mode: 0o644, Mtime: 1, Uid: 1000, Gid: 1000},
			{Path: "data", Type: 53, Mode: 0o755, Mtime: 2},
		},
		Submit: &apiv1.SubmitOptions{},
	}
}

func commandDigest(t *testing.T, req *apiv1.LaunchCommandRequest) string {
	s, err := NewCommand(1, req, nil)
	require.NoError(t, err)
	require.NotEmpty(t, s.digest)
	return s.digest
}

func TestDigestPinsCanonicalForm(t *testing.T) {
	config, err := structpb.NewStruct(map[string]any{"b": 1, "a": "<x>"})
	require.NoError(t, err)
	req := &apiv1.LaunchCommandRequest{Config: config, Submit: &apiv1.SubmitOptions{}}

	canonical := `{"admission":"QUEUE","config":{"a":"<x>","b":1},"files":[],"kind":"COMMAND",` +
		`"template_name":"","workspace_id":0}`
	sum := sha256.Sum256([]byte(canonical))
	require.Equal(t, hex.EncodeToString(sum[:]), commandDigest(t, req))
}

func TestCommandDigest(t *testing.T) {
	base := commandDigest(t, commandRequest(t))
	require.Len(t, base, 64)
	require.Equal(t, base, commandDigest(t, commandRequest(t)), "the digest is stable")

	unchanged := map[string]func(*apiv1.LaunchCommandRequest){
		"key": func(r *apiv1.LaunchCommandRequest) { r.Submit.IdempotencyKey = "k1" },
		"dry run": func(r *apiv1.LaunchCommandRequest) {
			r.Submit.DryRun = true
		},
		"expected digest": func(r *apiv1.LaunchCommandRequest) { r.Submit.ExpectedDigest = "abc" },
		"explicit queue admission": func(r *apiv1.LaunchCommandRequest) {
			r.Submit.Admission = apiv1.Admission_ADMISSION_QUEUE
		},
		"file mtime, uid, and gid": func(r *apiv1.LaunchCommandRequest) {
			r.Files[0].Mtime, r.Files[0].Uid, r.Files[0].Gid = 99, 0, 0
		},
		"file order": func(r *apiv1.LaunchCommandRequest) {
			r.Files[0], r.Files[1] = r.Files[1], r.Files[0]
		},
		"unused data": func(r *apiv1.LaunchCommandRequest) { r.Data = []byte("x") },
	}
	for name, change := range unchanged {
		req := commandRequest(t)
		change(req)
		require.Equal(t, base, commandDigest(t, req), name)
	}

	changed := map[string]func(*apiv1.LaunchCommandRequest){
		"config": func(r *apiv1.LaunchCommandRequest) {
			r.Config.Fields["resources"].GetStructValue().Fields["slots"] = structpb.NewNumberValue(2)
		},
		"file content": func(r *apiv1.LaunchCommandRequest) { r.Files[0].Content = []byte("print(2)") },
		"file mode":    func(r *apiv1.LaunchCommandRequest) { r.Files[0].Mode = 0o755 },
		"file path":    func(r *apiv1.LaunchCommandRequest) { r.Files[0].Path = "main.py" },
		"workspace":    func(r *apiv1.LaunchCommandRequest) { r.WorkspaceId = 3 },
		"template":     func(r *apiv1.LaunchCommandRequest) { r.TemplateName = "t" },
	}
	for name, change := range changed {
		req := commandRequest(t)
		change(req)
		require.NotEqual(t, base, commandDigest(t, req), name)
	}

	// A shell with the same content is a different request.
	req := commandRequest(t)
	shell, err := NewShell(1, &apiv1.LaunchShellRequest{
		Config: req.Config, WorkspaceId: req.WorkspaceId, Files: req.Files, Submit: req.Submit,
	}, nil)
	require.NoError(t, err)
	require.NotEqual(t, base, shell.digest)
}

func TestAdmissionIsInTheDigest(t *testing.T) {
	queue, err := requestDigest(model.JobTypeCommand, model.AdmissionQueue, map[string]any{})
	require.NoError(t, err)
	immediate, err := requestDigest(model.JobTypeCommand, model.AdmissionImmediate, map[string]any{})
	require.NoError(t, err)
	require.NotEqual(t, queue, immediate)
}

func experimentDigest(t *testing.T, req *apiv1.CreateExperimentRequest) string {
	s, err := NewExperiment(1, req, nil)
	require.NoError(t, err)
	return s.digest
}

func TestExperimentDigest(t *testing.T) {
	config := "entrypoint: python train.py\nsearcher:\n  name: single\n  metric: loss\n" +
		"resources:\n  slots_per_trial: 2\n"
	reordered := "# a comment\nresources: {slots_per_trial: 2.0}\nsearcher:\n  metric: loss\n" +
		"  name: single\nentrypoint: 'python train.py'\n"
	req := func(config string) *apiv1.CreateExperimentRequest {
		return &apiv1.CreateExperimentRequest{
			Config:          config,
			ProjectId:       3,
			Activate:        true,
			ModelDefinition: []*utilv1.File{{Path: "train.py", Content: []byte("x"), Mtime: 5}},
			Submit:          &apiv1.SubmitOptions{},
		}
	}

	base := experimentDigest(t, req(config))
	require.Equal(t, base, experimentDigest(t, req(reordered)),
		"formatting, comments, key order, and number spelling do not change the digest")

	validateOnly := req(config)
	validateOnly.ValidateOnly = true
	require.Equal(t, base, experimentDigest(t, validateOnly))

	for name, change := range map[string]func(*apiv1.CreateExperimentRequest){
		"config":   func(r *apiv1.CreateExperimentRequest) { r.Config += "max_restarts: 1\n" },
		"activate": func(r *apiv1.CreateExperimentRequest) { r.Activate = false },
		"project":  func(r *apiv1.CreateExperimentRequest) { r.ProjectId = 1 },
		"parent":   func(r *apiv1.CreateExperimentRequest) { r.ParentId = 7 },
		"template": func(r *apiv1.CreateExperimentRequest) { r.Template = ptrs.Ptr("t") },
		"model definition": func(r *apiv1.CreateExperimentRequest) {
			r.ModelDefinition[0].Content = []byte("y")
		},
	} {
		r := req(config)
		change(r)
		require.NotEqual(t, base, experimentDigest(t, r), name)
	}

	_, err := NewExperiment(1, req("entrypoint: [unclosed"), nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGenericTaskDigest(t *testing.T) {
	req := func() *apiv1.CreateGenericTaskRequest {
		return &apiv1.CreateGenericTaskRequest{
			Config:    "entrypoint: [python, run.py]\nresources: {slots: 1}\n",
			ProjectId: ptrs.Ptr(int32(4)),
			Submit:    &apiv1.SubmitOptions{},
		}
	}
	digestOf := func(r *apiv1.CreateGenericTaskRequest) string {
		s, err := NewGenericTask(1, r)
		require.NoError(t, err)
		return s.digest
	}

	base := digestOf(req())
	for name, change := range map[string]func(*apiv1.CreateGenericTaskRequest){
		"no pause":        func(r *apiv1.CreateGenericTaskRequest) { r.NoPause = ptrs.Ptr(false) },
		"forked from":     func(r *apiv1.CreateGenericTaskRequest) { r.ForkedFrom = ptrs.Ptr("t") },
		"parent":          func(r *apiv1.CreateGenericTaskRequest) { r.ParentId = ptrs.Ptr("t") },
		"inherit context": func(r *apiv1.CreateGenericTaskRequest) { r.InheritContext = ptrs.Ptr(true) },
		"project":         func(r *apiv1.CreateGenericTaskRequest) { r.ProjectId = nil },
		"config":          func(r *apiv1.CreateGenericTaskRequest) { r.Config += "debug: true\n" },
	} {
		r := req()
		change(r)
		require.NotEqual(t, base, digestOf(r), name)
	}
}

func TestNewSubmissionOptions(t *testing.T) {
	immediate := &apiv1.SubmitOptions{Admission: apiv1.Admission_ADMISSION_IMMEDIATE}

	_, err := NewCommand(1, &apiv1.LaunchCommandRequest{Submit: immediate}, nil)
	require.Equal(t, codes.Unimplemented, status.Code(err))
	_, err = NewShell(1, &apiv1.LaunchShellRequest{Submit: immediate}, nil)
	require.Equal(t, codes.Unimplemented, status.Code(err))
	_, err = NewGenericTask(1, &apiv1.CreateGenericTaskRequest{Submit: immediate})
	require.Equal(t, codes.Unimplemented, status.Code(err))
	for _, dryRun := range []bool{false, true} {
		opts := proto.Clone(immediate).(*apiv1.SubmitOptions)
		opts.DryRun = dryRun
		_, err = NewExperiment(1, &apiv1.CreateExperimentRequest{Submit: opts}, nil)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}

	_, err = NewCommand(1, &apiv1.LaunchCommandRequest{
		Submit: &apiv1.SubmitOptions{Admission: apiv1.Admission(9)},
	}, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = NewCommand(1, &apiv1.LaunchCommandRequest{
		Submit: &apiv1.SubmitOptions{IdempotencyKey: "not a key"},
	}, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = NewExperiment(1, &apiv1.CreateExperimentRequest{
		Unmanaged: ptrs.Ptr(true), Submit: &apiv1.SubmitOptions{},
	}, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// Without submit options, a create has no digest and no submission result.
	s, err := NewExperiment(1, &apiv1.CreateExperimentRequest{ValidateOnly: true}, nil)
	require.NoError(t, err)
	require.True(t, s.DryRun())
	require.Empty(t, s.digest)
	require.Nil(t, s.result())

	s, err = NewCommand(1, &apiv1.LaunchCommandRequest{}, nil)
	require.NoError(t, err)
	require.False(t, s.DryRun())
	require.Nil(t, s.result())
}

type testResponse struct {
	result *apiv1.SubmitResult
}

func unexpected[R any](t *testing.T, step string) func(context.Context, *apiv1.SubmitResult) (R, error) {
	return func(context.Context, *apiv1.SubmitResult) (R, error) {
		var zero R
		t.Errorf("unexpected step %s", step)
		return zero, fmt.Errorf("unexpected step %s", step)
	}
}

func TestRunDryRun(t *testing.T) {
	req := commandRequest(t)
	req.Submit.DryRun = true
	s, err := NewCommand(1, req, nil)
	require.NoError(t, err)

	prepared := false
	resp, err := Run(context.Background(), s, Handler[*testResponse]{
		Prepare: func(context.Context) error {
			prepared = true
			return nil
		},
		DryRun: func(_ context.Context, result *apiv1.SubmitResult) (*testResponse, error) {
			return &testResponse{result: result}, nil
		},
		Commit: func(context.Context, bun.Tx) error {
			t.Error("a dry run must not commit")
			return fmt.Errorf("committed a dry run")
		},
		Respond: unexpected[*testResponse](t, "respond"),
	})
	require.NoError(t, err)
	require.True(t, prepared)
	require.Equal(t, s.digest, resp.result.RequestDigest)
	require.Empty(t, resp.result.JobId)
	require.False(t, resp.result.Replayed)
	require.Equal(t, apiv1.AdmissionOutcome_ADMISSION_OUTCOME_UNSPECIFIED, resp.result.Outcome)
}

func TestRunPlanChanged(t *testing.T) {
	req := commandRequest(t)
	req.Submit.ExpectedDigest = strings.Repeat("0", 64)
	s, err := NewCommand(1, req, nil)
	require.NoError(t, err)

	_, err = Run(context.Background(), s, Handler[*testResponse]{
		Prepare: func(context.Context) error {
			t.Error("a changed plan must fail before parsing")
			return fmt.Errorf("parsed a changed plan")
		},
		DryRun:  unexpected[*testResponse](t, "dry run"),
		Respond: unexpected[*testResponse](t, "respond"),
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.True(t, strings.HasPrefix(status.Convert(err).Message(), "plan_changed:"))
	require.Contains(t, status.Convert(err).Message(), s.digest)
}

func TestDispatchSerializesAJob(t *testing.T) {
	jobs := []model.JobID{model.NewJobID(), model.NewJobID()}
	var mu sync.Mutex
	running := map[model.JobID]int{}
	most := map[model.JobID]int{}
	start := func(ctx context.Context, jobID model.JobID) error {
		mu.Lock()
		running[jobID]++
		if running[jobID] > most[jobID] {
			most[jobID] = running[jobID]
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		running[jobID]--
		mu.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, jobID := range jobs {
			wg.Add(1)
			go func(jobID model.JobID) {
				defer wg.Done()
				require.NoError(t, Dispatch(context.Background(), jobID, start))
			}(jobID)
		}
	}
	wg.Wait()
	for _, jobID := range jobs {
		require.Equal(t, 1, most[jobID], "dispatches of one job never overlap")
	}

	jobLocks.Lock()
	defer jobLocks.Unlock()
	require.Empty(t, jobLocks.held, "released job locks are forgotten")
}

func TestDispatchIsDetached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, Dispatch(ctx, model.NewJobID(), func(ctx context.Context, _ model.JobID) error {
		return ctx.Err()
	}))
}

func TestTemplateContentIsInTheDigest(t *testing.T) {
	templates := map[string]*model.Template{}
	read := func(name string) (*model.Template, error) { return templates[name], nil }
	digestOf := func(name string) (string, *Submission) {
		req := commandRequest(t)
		req.TemplateName = name
		s, err := NewCommand(1, req, read)
		require.NoError(t, err)
		return s.digest, s
	}

	templates["t"] = &model.Template{Name: "t", Config: []byte(`{"resources": {"slots": 1}}`)}
	base, s := digestOf("t")
	require.Equal(t, templates["t"], s.Template(), "the create applies the template the digest read")

	// The same content in another form is the same template.
	templates["t"] = &model.Template{Name: "t", Config: []byte("resources:\n  slots: 1.0\n")}
	same, _ := digestOf("t")
	require.Equal(t, base, same)

	templates["t"] = &model.Template{Name: "t", Config: []byte(`{"resources": {"slots": 2}}`)}
	changed, _ := digestOf("t")
	require.NotEqual(t, base, changed, "a changed template changes the digest")

	// A template that cannot be read digests as its name alone, and the create reads it itself.
	missing, s := digestOf("missing")
	require.Nil(t, s.Template())
	unread, err := NewCommand(1, func() *apiv1.LaunchCommandRequest {
		req := commandRequest(t)
		req.TemplateName = "missing"
		return req
	}(), nil)
	require.NoError(t, err)
	require.Equal(t, missing, unread.digest)

	// Without submit options, no template is read.
	_, err = NewCommand(1, &apiv1.LaunchCommandRequest{TemplateName: "t"},
		func(string) (*model.Template, error) {
			t.Error("a create without submit options read its template for a digest")
			return nil, nil
		})
	require.NoError(t, err)

	_, err = NewExperiment(1, &apiv1.CreateExperimentRequest{
		Config: "entrypoint: x\n", Template: ptrs.Ptr("t"), Submit: &apiv1.SubmitOptions{},
	}, func(string) (*model.Template, error) { return nil, fmt.Errorf("the database is down") })
	require.ErrorContains(t, err, "the database is down")
}
