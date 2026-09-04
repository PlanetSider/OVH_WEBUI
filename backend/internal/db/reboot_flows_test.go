package db

import (
	"testing"
	"time"
)

func TestBotRebootFlowTransitionIsActorBoundAndOneTime(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	row := BotRebootFlowRow{
		ID: "flow-1", Channel: "telegram", ActorID: "user-1", ChatID: "chat-1",
		Stage: "confirm", Payload: `{"service":"server-1"}`,
	}
	if err := database.CreateBotRebootFlow(row); err != nil {
		t.Fatal(err)
	}
	if ok, err := database.TransitionBotRebootFlow("flow-1", "telegram", "other-user", "chat-1", "confirm", "done", row.Payload); err != nil || ok {
		t.Fatalf("other actor transition ok=%v err=%v, want false, nil", ok, err)
	}
	if ok, err := database.TransitionBotRebootFlow("flow-1", "telegram", "user-1", "chat-1", "confirm", "done", row.Payload); err != nil || !ok {
		t.Fatalf("first transition ok=%v err=%v, want true, nil", ok, err)
	}
	if ok, err := database.TransitionBotRebootFlow("flow-1", "telegram", "user-1", "chat-1", "confirm", "done", row.Payload); err != nil || ok {
		t.Fatalf("duplicate transition ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestExpiredBotRebootFlowCannotBeReadOrAdvanced(t *testing.T) {
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	createdAt := float64(time.Now().Add(-BotRebootFlowTTL - time.Minute).Unix())
	row := BotRebootFlowRow{
		ID: "expired-flow", Channel: "feishu", ActorID: "user-1", ChatID: "user-1",
		Stage: "account", Payload: `{}`, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := database.CreateBotRebootFlow(row); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := database.GetBotRebootFlow("expired-flow", "feishu", "user-1", "user-1"); err != nil || ok {
		t.Fatalf("expired get ok=%v err=%v, want false, nil", ok, err)
	}
	if ok, err := database.TransitionBotRebootFlow("expired-flow", "feishu", "user-1", "user-1", "account", "loading", `{}`); err != nil || ok {
		t.Fatalf("expired transition ok=%v err=%v, want false, nil", ok, err)
	}
}
