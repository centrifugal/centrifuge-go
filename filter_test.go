package centrifuge

// Tests for publication filtering (server-side filtering by publication tags).
// The Filter* helpers build a protocol FilterNode tree; the subscribe request
// carries it in the Tf field. The feature requires Centrifugo PRO / namespace
// config, so wire-level behavior is exercised against the in-process FakeServer.

import (
	"testing"
)

func TestFilterBuilderLeafNodes(t *testing.T) {
	cases := []struct {
		node    *FilterNode
		wantCmp string
		wantVal string
	}{
		{FilterEq("ticker", "AAPL"), "eq", "AAPL"},
		{FilterNeq("source", "TEST"), "neq", "TEST"},
		{FilterExists("price"), "ex", ""},
		{FilterNotExists("internal_id"), "nex", ""},
		{FilterStartsWith("ticker", "AA"), "sw", "AA"},
		{FilterEndsWith("source", "DAQ"), "ew", "DAQ"},
		{FilterContains("category", "ec"), "ct", "ec"},
		{FilterGt("price", "100"), "gt", "100"},
		{FilterGte("volume", "1000"), "gte", "1000"},
		{FilterLt("price", "200"), "lt", "200"},
		{FilterLte("volume", "1000"), "lte", "1000"},
	}
	for _, c := range cases {
		if c.node.node.Cmp != c.wantCmp {
			t.Fatalf("cmp = %q, want %q", c.node.node.Cmp, c.wantCmp)
		}
		if c.node.node.Val != c.wantVal {
			t.Fatalf("val = %q, want %q", c.node.node.Val, c.wantVal)
		}
	}
}

func TestFilterBuilderSetMembership(t *testing.T) {
	in := FilterIn("category", "tech", "finance")
	if in.node.Cmp != "in" || len(in.node.Vals) != 2 || in.node.Vals[0] != "tech" {
		t.Fatalf("unexpected in node: %+v", in.node)
	}
	nin := FilterNin("ticker", "MSFT", "GOOGL")
	if nin.node.Cmp != "nin" || len(nin.node.Vals) != 2 {
		t.Fatalf("unexpected nin node: %+v", nin.node)
	}
}

func TestFilterBuilderLogicalNodes(t *testing.T) {
	filter := FilterAnd(
		FilterEq("ticker", "AAPL"),
		FilterGte("price", "100"),
		FilterIn("source", "NASDAQ", "NYSE"),
	)
	if filter.node.Op != "and" || len(filter.node.Nodes) != 3 {
		t.Fatalf("unexpected and node: %+v", filter.node)
	}
	if filter.node.Nodes[0].Key != "ticker" || filter.node.Nodes[2].Cmp != "in" {
		t.Fatalf("unexpected and children: %+v", filter.node.Nodes)
	}

	or := FilterOr(FilterEq("ticker", "MSFT"), FilterEq("category", "tech"))
	if or.node.Op != "or" || len(or.node.Nodes) != 2 {
		t.Fatalf("unexpected or node: %+v", or.node)
	}

	not := FilterNot(FilterEq("source", "NYSE"))
	if not.node.Op != "not" || len(not.node.Nodes) != 1 || not.node.Nodes[0].Cmp != "eq" {
		t.Fatalf("unexpected not node: %+v", not.node)
	}
}

func TestNewSubscriptionRejectsDeltaWithTagsFilter(t *testing.T) {
	client := NewProtobufClient("ws://localhost:0/connection/websocket", Config{})
	defer client.Close()
	_, err := client.NewSubscription("market:stocks", SubscriptionConfig{
		Delta:      DeltaTypeFossil,
		TagsFilter: FilterEq("ticker", "AAPL"),
	})
	if err == nil {
		t.Fatal("expected error combining delta and tags filter")
	}
}

func TestSetTagsFilterRejectsDeltaCombination(t *testing.T) {
	client := NewProtobufClient("ws://localhost:0/connection/websocket", Config{})
	defer client.Close()
	sub, err := client.NewSubscription("market:stocks", SubscriptionConfig{Delta: DeltaTypeFossil})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	if err := sub.SetTagsFilter(FilterEq("ticker", "AAPL")); err == nil {
		t.Fatal("expected error combining delta and tags filter")
	}
}

func TestSubscribeRequestCarriesTagsFilter(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("market:stocks", SubscriptionConfig{
		TagsFilter: FilterAnd(
			FilterEq("ticker", "AAPL"),
			FilterGte("price", "100"),
		),
	})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 1)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	tf := server.LastSubscribe().Tf
	if tf == nil {
		t.Fatal("subscribe request must carry the tags filter")
	}
	if tf.Op != "and" || len(tf.Nodes) != 2 {
		t.Fatalf("unexpected tf: %+v", tf)
	}
	if tf.Nodes[0].Key != "ticker" || tf.Nodes[0].Cmp != "eq" || tf.Nodes[0].Val != "AAPL" {
		t.Fatalf("unexpected tf leaf: %+v", tf.Nodes[0])
	}
	if tf.Nodes[1].Cmp != "gte" {
		t.Fatalf("unexpected tf leaf: %+v", tf.Nodes[1])
	}
}

func TestSetTagsFilterAppliesOnSubscribe(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("market:stocks", SubscriptionConfig{})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	if err := sub.SetTagsFilter(FilterEq("ticker", "BTC")); err != nil {
		t.Fatalf("set tags filter: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 1)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	tf := server.LastSubscribe().Tf
	if tf == nil || tf.Key != "ticker" || tf.Val != "BTC" {
		t.Fatalf("unexpected tf: %+v", tf)
	}
}

func TestSubscribeWithoutFilterSendsNoTf(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("market:stocks", SubscriptionConfig{})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 1)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	if tf := server.LastSubscribe().Tf; tf != nil {
		t.Fatalf("expected no tags filter, got %+v", tf)
	}
}
