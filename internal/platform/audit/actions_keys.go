package audit

// The master-key actions: resealing the sealed stores under the current key
// before an older one is retired. Store names, key ids and counts are values;
// a sealed value, a ref and an address never are.
const (
	// KeysReseal re-sealed the rows of one store's batch under the current key.
	KeysReseal Action = "keys.reseal"
)

func init() {
	register(KeysReseal, "master_key", classAPIActivity, activityUnknown, "store", "key_id", "from_key_ids", "count")
}
