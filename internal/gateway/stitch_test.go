package gateway

import (
	"context"
	"testing"

	"microagent2/internal/response"
)

func makeInput(msgs ...response.InputItem) []response.InputItem {
	return msgs
}

func userMsg(text string) response.InputItem {
	return response.InputItem{Type: "message", Role: "user", Content: text}
}

func asstMsg(text string) response.InputItem {
	return response.InputItem{Type: "message", Role: "assistant", Content: text}
}

func TestDecideSession_SingleMessageDoesNotStitch(t *testing.T) {
	srv, _ := newTestServer(t)
	d, herr := srv.decideSession(context.Background(), "", true, makeInput(userMsg("hi")))
	if herr != nil {
		t.Fatalf("unexpected err: %+v", herr)
	}
	if d.StitchPrefixHash != "" {
		t.Fatalf("single-message input should not trigger stitch lookup; got hash=%q", d.StitchPrefixHash)
	}
	if d.Stitched {
		t.Fatal("single-message input should never set Stitched=true")
	}
	if d.SessionID == "" {
		t.Fatal("should mint a session id")
	}
}

func TestDecideSession_PrevIdMissingReturnsError(t *testing.T) {
	srv, _ := newTestServer(t)
	_, herr := srv.decideSession(context.Background(), "resp_doesnotexist", true, makeInput(userMsg("hi")))
	if herr == nil {
		t.Fatal("expected handler error for unknown previous_response_id")
	}
	if herr.code != "invalid_request" || herr.status != 400 {
		t.Fatalf("wrong error shape: %+v", herr)
	}
}

func TestDecideSession_PrevIdPresentSkipsStitch(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Pre-seed a response so InheritSessionID works.
	r := &response.Response{ID: "resp_seed", SessionID: "sess-seed", Status: response.StatusCompleted}
	if err := srv.responses.Save(ctx, r); err != nil {
		t.Fatal(err)
	}

	d, herr := srv.decideSession(ctx, "resp_seed", true, makeInput(
		userMsg("hi"), asstMsg("hey"), userMsg("next"),
	))
	if herr != nil {
		t.Fatalf("unexpected err: %+v", herr)
	}
	if d.SessionID != "sess-seed" {
		t.Fatalf("expected inherited session-seed, got %q", d.SessionID)
	}
	if d.StitchPrefixHash != "" {
		t.Fatal("prev-id path must not compute stitch hash")
	}
	if d.EffectivePrevRespID != "resp_seed" {
		t.Fatalf("EffectivePrevRespID: want resp_seed, got %q", d.EffectivePrevRespID)
	}
}

func TestDecideSession_StoreFalseSkipsStitch(t *testing.T) {
	srv, _ := newTestServer(t)
	d, herr := srv.decideSession(context.Background(), "", false, makeInput(
		userMsg("hi"), asstMsg("hey"), userMsg("next"),
	))
	if herr != nil {
		t.Fatalf("unexpected err: %+v", herr)
	}
	if d.StitchPrefixHash != "" {
		t.Fatalf("store=false must not trigger stitching; got hash=%q", d.StitchPrefixHash)
	}
}

func TestDecideSession_StitchMissMintsFreshAndRecordsHash(t *testing.T) {
	srv, _ := newTestServer(t)
	input := makeInput(
		userMsg("hi"), asstMsg("hey"), userMsg("next"),
	)
	d, herr := srv.decideSession(context.Background(), "", true, input)
	if herr != nil {
		t.Fatalf("unexpected err: %+v", herr)
	}
	if d.Stitched {
		t.Fatal("fresh turn must not report Stitched=true")
	}
	if d.SessionID == "" {
		t.Fatal("should mint session id")
	}
	if d.StitchPrefixHash == "" {
		t.Fatal("should compute stitch hash for multi-message input")
	}
	expected := response.StitchHash(input[:len(input)-1])
	if d.StitchPrefixHash != expected {
		t.Fatalf("hash mismatch: got %s want %s", d.StitchPrefixHash, expected)
	}
}

func TestDecideSession_StitchHitReusesSessionAndSetsPrevRespID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Pre-seed: a session with one stored response AND an index entry
	// keyed by the hash of a "turn 1" shape.
	turn1 := makeInput(userMsg("hi"), asstMsg("hey"))
	hashAtTurn1End := response.StitchHash(turn1)

	prior := &response.Response{
		ID:        "resp_prior",
		SessionID: "sess-abc",
		Input:     []response.InputItem{userMsg("hi")},
		Output:    []response.OutputItem{{Type: "message", Role: "assistant", Content: []response.ContentPart{{Type: "output_text", Text: "hey"}}}},
		Status:    response.StatusCompleted,
	}
	if err := srv.responses.Save(ctx, prior); err != nil {
		t.Fatal(err)
	}
	if err := srv.responses.StoreSessionPrefixHash(ctx, hashAtTurn1End, "sess-abc"); err != nil {
		t.Fatal(err)
	}

	// Now replay: [u(hi), a(hey), u(next)]. Prefix = turn1 → should hit.
	input := makeInput(userMsg("hi"), asstMsg("hey"), userMsg("next"))
	d, herr := srv.decideSession(ctx, "", true, input)
	if herr != nil {
		t.Fatalf("unexpected err: %+v", herr)
	}
	if !d.Stitched {
		t.Fatal("expected Stitched=true on hash hit")
	}
	if d.SessionID != "sess-abc" {
		t.Fatalf("SessionID: want sess-abc, got %q", d.SessionID)
	}
	if d.EffectivePrevRespID != "resp_prior" {
		t.Fatalf("EffectivePrevRespID: want resp_prior, got %q", d.EffectivePrevRespID)
	}
	if d.StitchPrefixHash == "" {
		t.Fatal("StitchPrefixHash should be populated on hit")
	}
}

func TestWriteStitchIndex_StoresHashOfFullTurn(t *testing.T) {
	srv, mr := newTestServer(t)
	ctx := context.Background()

	input := []response.InputItem{userMsg("hi")}
	output := []response.OutputItem{
		{Type: "message", Role: "assistant", Content: []response.ContentPart{{Type: "output_text", Text: "hey"}}},
	}
	srv.writeStitchIndex(ctx, "sess-X", "corr-1", input, output)

	// Recompute the hash as the NEXT turn's prefix lookup would:
	// the full conversation after this turn is [u(hi), a(hey)]
	full := []response.InputItem{
		userMsg("hi"),
		response.OutputItemToInputItem(output[0]),
	}
	want := response.StitchHash(full)
	got, err := mr.Get("session_hash:" + want)
	if err != nil {
		t.Fatalf("miniredis lookup: %v", err)
	}
	if got != "sess-X" {
		t.Fatalf("stored value: want sess-X got %q", got)
	}
}
