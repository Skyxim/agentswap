package transfer

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/WellWells/agentswap/internal/swap"
)

// Export collects all saved accounts from the provided managers and exports their native snapshots into a Bundle.
func Export(mgrs map[string]*swap.Manager) (Bundle, error) {
	bundle := make(Bundle)

	// Collect keys in deterministic order
	keys := make([]string, 0, len(mgrs))
	for k := range mgrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, provider := range keys {
		mgr := mgrs[provider]
		if mgr == nil {
			continue
		}

		r, err := mgr.S.Load()
		if err != nil {
			return nil, fmt.Errorf("load registry for %s: %w", provider, err)
		}

		if len(r.Accounts) == 0 {
			continue
		}

		items := make([]AccountItem, 0, len(r.Accounts))
		for _, a := range r.Accounts {
			snap, err := mgr.S.ReadSnapshot(a.Key)
			if err != nil {
				return nil, fmt.Errorf("read snapshot for %s (%s): %w", provider, a.Key, err)
			}

			items = append(items, AccountItem{
				Alias: a.Alias,
				Token: json.RawMessage(snap),
			})
		}

		bundle[provider] = items
	}

	return bundle, nil
}
