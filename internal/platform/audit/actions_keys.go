package audit

// The master-key actions: resealing the sealed stores under the current key
// before an older one is retired. Store names, key ids and counts are values;
// a sealed value, a ref and an address never are.
const (
	// KeysReseal re-sealed the rows of one store's batch under the current key.
	KeysReseal Action = "keys.reseal"
	// KeysWrap is one data key the worker wrapped for brain.
	KeysWrap Action = "keys.wrap"
	// KeysUnwrap is one data key the worker unwrapped, or refused to.
	KeysUnwrap Action = "keys.unwrap"
	// KeysProviderTest is one provider test the worker ran with a saved key.
	KeysProviderTest Action = "keys.provider_test"
	// KeysPasswordVerify is one password check the worker ran for brain, or
	// refused to.
	KeysPasswordVerify Action = "keys.password_verify"
)

func init() {
	register(KeysReseal, "master_key", classAPIActivity, activityUnknown, "store", "key_id", "from_key_ids", "count")
	// The worker writes these through audit.record_keys, never through Record,
	// in the same {fields, values} shape, so the sealer keeps the values.
	for _, a := range []Action{KeysWrap, KeysUnwrap, KeysProviderTest, KeysPasswordVerify} {
		register(a, "data_key", classAPIActivity, activityUnknown, "account_id", "audience", "kind", "op", "ref", "ref_digest", "sandbox_id")
	}
}
