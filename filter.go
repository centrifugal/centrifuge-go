package centrifuge

import "github.com/centrifugal/protocol"

// FilterNode is a node in a publication tags-filter expression tree, used for
// server-side publication filtering. Build instances with the Filter* helpers
// and pass the result via SubscriptionConfig.TagsFilter or
// Subscription.SetTagsFilter.
//
// A filter is either a leaf node (a comparison such as ticker == "AAPL") or a
// logical node combining child nodes with and/or/not. The server evaluates the
// filter against each publication's tags and delivers only matching
// publications. See https://centrifugal.dev/docs/server/publication_filtering.
//
// Publication filtering must be enabled for the namespace on the server
// (allow_tags_filter) and cannot be combined with delta compression.
type FilterNode struct {
	node *protocol.FilterNode
}

func leafFilter(key, cmp, val string, vals []string) *FilterNode {
	return &FilterNode{node: &protocol.FilterNode{Key: key, Cmp: cmp, Val: val, Vals: vals}}
}

func logicalFilter(op string, nodes []*FilterNode) *FilterNode {
	children := make([]*protocol.FilterNode, 0, len(nodes))
	for _, n := range nodes {
		children = append(children, n.node)
	}
	return &FilterNode{node: &protocol.FilterNode{Op: op, Nodes: children}}
}

// FilterEq matches when tag key equals val.
func FilterEq(key, val string) *FilterNode { return leafFilter(key, "eq", val, nil) }

// FilterNeq matches when tag key does not equal val.
func FilterNeq(key, val string) *FilterNode { return leafFilter(key, "neq", val, nil) }

// FilterIn matches when tag key is one of vals.
func FilterIn(key string, vals ...string) *FilterNode { return leafFilter(key, "in", "", vals) }

// FilterNin matches when tag key is not one of vals.
func FilterNin(key string, vals ...string) *FilterNode { return leafFilter(key, "nin", "", vals) }

// FilterExists matches when tag key exists.
func FilterExists(key string) *FilterNode { return leafFilter(key, "ex", "", nil) }

// FilterNotExists matches when tag key does not exist.
func FilterNotExists(key string) *FilterNode { return leafFilter(key, "nex", "", nil) }

// FilterStartsWith matches when string tag key starts with val.
func FilterStartsWith(key, val string) *FilterNode { return leafFilter(key, "sw", val, nil) }

// FilterEndsWith matches when string tag key ends with val.
func FilterEndsWith(key, val string) *FilterNode { return leafFilter(key, "ew", val, nil) }

// FilterContains matches when string tag key contains val.
func FilterContains(key, val string) *FilterNode { return leafFilter(key, "ct", val, nil) }

// FilterGt matches when numeric tag key is greater than val.
func FilterGt(key, val string) *FilterNode { return leafFilter(key, "gt", val, nil) }

// FilterGte matches when numeric tag key is greater than or equal to val.
func FilterGte(key, val string) *FilterNode { return leafFilter(key, "gte", val, nil) }

// FilterLt matches when numeric tag key is less than val.
func FilterLt(key, val string) *FilterNode { return leafFilter(key, "lt", val, nil) }

// FilterLte matches when numeric tag key is less than or equal to val.
func FilterLte(key, val string) *FilterNode { return leafFilter(key, "lte", val, nil) }

// FilterAnd matches when all nodes match.
func FilterAnd(nodes ...*FilterNode) *FilterNode { return logicalFilter("and", nodes) }

// FilterOr matches when at least one of nodes matches.
func FilterOr(nodes ...*FilterNode) *FilterNode { return logicalFilter("or", nodes) }

// FilterNot inverts node.
func FilterNot(node *FilterNode) *FilterNode { return logicalFilter("not", []*FilterNode{node}) }
