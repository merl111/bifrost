package cursorclient

import "errors"

const (
	blobIDLen             = 32 // SHA-256 content address used by Cursor's blob store
	maxBlobBytes          = 1 << 20
	maxBlobCount          = 256
	maxBlobStoreBytes     = 8 << 20
	maxPersistedBlobBytes = 4 << 20
	maxPrefetchBytes      = 2 << 20
)

// CheckpointBlobsPresent reports whether every blob id in a saved conversation
// checkpoint is available to send back. An empty checkpoint is ready. A
// checkpoint that names blobs this process never stored is not.
func CheckpointBlobsPresent(checkpoint []byte, blobs map[string][]byte) bool {
	if len(checkpoint) == 0 {
		return true
	}
	state, err := Decode("ConversationStateStructure", checkpoint)
	if err != nil {
		return false
	}
	for _, id := range conversationBlobIDs(state) {
		if _, ok := blobs[string(id)]; !ok {
			return false
		}
	}
	return true
}

func (e *ToolEnv) seedBlobs(blobs map[string][]byte) {
	for id, data := range blobs {
		_ = e.rememberBlob(id, data)
	}
}

func (e *ToolEnv) rememberBlob(id string, data []byte) error {
	if id == "" || len(data) == 0 {
		return nil
	}
	if len(data) > maxBlobBytes {
		return errors.New("blob is too large")
	}
	e.prepare()
	for len(e.blobs) >= maxBlobCount {
		if _, ok := e.blobs[id]; ok {
			break
		}
		for old := range e.blobs {
			delete(e.blobs, old)
			break
		}
	}
	e.blobs[id] = append([]byte(nil), data...)
	for e.blobBytes() > maxBlobStoreBytes && len(e.blobs) > 1 {
		for old := range e.blobs {
			if old == id {
				continue
			}
			delete(e.blobs, old)
			break
		}
	}
	return nil
}

func (e *ToolEnv) blobBytes() int {
	n := 0
	for _, data := range e.blobs {
		n += len(data)
	}
	return n
}

func persistedBlobs(blobs map[string][]byte, checkpoint []byte) map[string][]byte {
	if len(blobs) == 0 {
		return nil
	}
	var prefer []string
	if state, err := Decode("ConversationStateStructure", checkpoint); err == nil {
		for _, id := range conversationBlobIDs(state) {
			prefer = append(prefer, string(id))
		}
	}
	out := map[string][]byte{}
	total := 0
	add := func(id string) {
		if _, ok := out[id]; ok {
			return
		}
		data, ok := blobs[id]
		if !ok || len(data) == 0 || total+len(data) > maxPersistedBlobBytes {
			return
		}
		out[id] = append([]byte(nil), data...)
		total += len(data)
	}
	for _, id := range prefer {
		add(id)
	}
	for id := range blobs {
		add(id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func prefetchBlobs(state *Value, blobs map[string][]byte) []*Value {
	if state == nil || len(blobs) == 0 {
		return nil
	}
	var out []*Value
	total := 0
	seen := map[string]struct{}{}
	for _, id := range conversationBlobIDs(state) {
		key := string(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		data, ok := blobs[key]
		if !ok || total+len(data) > maxPrefetchBytes {
			continue
		}
		item, err := New("PreFetchedBlob")
		if err != nil {
			continue
		}
		item.Set("id", append([]byte(nil), id...))
		item.Set("value", append([]byte(nil), data...))
		out = append(out, item)
		total += len(data)
	}
	return out
}

func conversationBlobIDs(state *Value) [][]byte {
	if state == nil {
		return nil
	}
	var ids [][]byte
	for _, name := range []string{
		"root_prompt_messages_json", "turns", "todos", "summary", "plan",
		"summary_archive", "summary_archives",
	} {
		for _, item := range asSlice(state.Get(name)) {
			if id, ok := blobID(item); ok {
				ids = append(ids, id)
			}
		}
	}
	for _, name := range []string{"file_states", "subagent_state_refs"} {
		entries, _ := state.Get(name).(map[string]any)
		for _, item := range entries {
			if id, ok := blobID(item); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func blobID(item any) ([]byte, bool) {
	id, ok := item.([]byte)
	return id, ok && len(id) == blobIDLen
}
