package cursorclient

import (
	"bytes"
	"strings"
	"testing"
)

func TestMissingBlobIsAnEmptyResult(t *testing.T) {
	env := &ToolEnv{}
	req, err := New("KvServerMessage")
	if err != nil {
		t.Fatal(err)
	}
	req.Set("id", int64(7))
	args, err := New("GetBlobArgs")
	if err != nil {
		t.Fatal(err)
	}
	args.Set("blob_id", bytes.Repeat([]byte{1}, blobIDLen))
	req.Set("get_blob_args", args)
	reply := env.kv(req)
	result := reply.Message("kv_client_message").Message("get_blob_result")
	if result == nil || result.Message("error") != nil || len(result.Bytes("blob_data")) != 0 {
		t.Fatalf("missing blob result error=%v data=%d", result.Message("error"), len(result.Bytes("blob_data")))
	}
	raw, err := reply.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "blob not found") {
		t.Fatal("missing blob was reported as an error")
	}

	stored := []byte("turn-body")
	if err := env.rememberBlob(string(args.Bytes("blob_id")), stored); err != nil {
		t.Fatal(err)
	}
	hit := env.kv(req).Message("kv_client_message").Message("get_blob_result")
	if !bytes.Equal(hit.Bytes("blob_data"), stored) {
		t.Fatalf("stored blob = %q", hit.Bytes("blob_data"))
	}
}

func TestCheckpointWithoutBlobsIsNotResumed(t *testing.T) {
	id := bytes.Repeat([]byte{0xab}, blobIDLen)
	state, err := New("ConversationStateStructure")
	if err != nil {
		t.Fatal(err)
	}
	state.Set("turns", []any{id})
	raw, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if CheckpointBlobsPresent(raw, nil) {
		t.Fatal("checkpoint with a missing turn blob was treated as ready")
	}
	if !CheckpointBlobsPresent(nil, nil) {
		t.Fatal("empty checkpoint should resume")
	}
	blobs := map[string][]byte{string(id): []byte("turn")}
	if !CheckpointBlobsPresent(raw, blobs) {
		t.Fatal("checkpoint whose blob is stored should resume")
	}

	msg, err := buildRun(RunInput{Prompt: "next", Model: "auto", Checkpoint: raw, Blobs: blobs}, "conv", "run")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode("AgentClientMessage", encoded)
	if err != nil {
		t.Fatal(err)
	}
	prefetched := decoded.Message("run_request").Messages("pre_fetched_blobs")
	if len(prefetched) != 1 || !bytes.Equal(prefetched[0].Bytes("id"), id) || string(prefetched[0].Bytes("value")) != "turn" {
		t.Fatalf("prefetch = %d", len(prefetched))
	}
}

func TestShortStateBytesAreNotBlobIDs(t *testing.T) {
	state, err := New("ConversationStateStructure")
	if err != nil {
		t.Fatal(err)
	}
	state.Set("summary", []byte("inline summary"))
	raw, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !CheckpointBlobsPresent(raw, nil) {
		t.Fatal("inline summary was treated as a missing blob")
	}
}
