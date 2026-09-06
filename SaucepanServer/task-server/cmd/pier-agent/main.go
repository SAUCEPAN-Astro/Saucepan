package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/saucepan/hotpath/internal/pierjob"
	"github.com/saucepan/hotpath/shared"
	"github.com/saucepan/hotpath/shared/alpaca"
	"github.com/saucepan/hotpath/shared/wire"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("pier-agent: %v", err)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.CaptureDir, 0o755); err != nil {
		return fmt.Errorf("create capture dir %s: %w", cfg.CaptureDir, err)
	}

	alpacaClient := alpaca.NewClient(cfg.AlpacaBaseURL)
	tel := alpaca.NewTelescope(alpacaClient, cfg.TelescopeNum)
	cam := alpaca.NewCamera(alpacaClient, cfg.CameraNum)
	fw := alpaca.NewFilterWheel(alpacaClient, cfg.FilterWheelNum)

	if err := tel.SetConnected(true); err != nil {
		return fmt.Errorf("connect telescope: %w", err)
	}
	if err := cam.SetConnected(true); err != nil {
		return fmt.Errorf("connect camera: %w", err)
	}
	hasFilterWheel := fw.SetConnected(true) == nil

	agent := NewAgent(cfg.NodeID, tel, cam, filterWheelOrNil(fw, hasFilterWheel), cfg.Safety, cfg.CaptureDir)
	if cfg.APIURL != "" && cfg.DeviceToken != "" {
		agent.Uploader = newR2Uploader(cfg.APIURL, cfg.DeviceToken, cfg.NodeID, cfg.UploadChunkSize)
		log.Printf("pier-agent: capture upload enabled (api=%s)", cfg.APIURL)
	}
	if cfg.PierCodeRunnerPath != "" {
		agent.PierCode = &pierCode{
			RunnerPath: cfg.PierCodeRunnerPath,
			CacheDir:   cfg.PierCodeCacheDir,
		}
		log.Printf("pier-agent: on-pier researcher code enabled (runner=%s, cache=%s)", cfg.PierCodeRunnerPath, cfg.PierCodeCacheDir)
	}

	statusTopic := fmt.Sprintf(wire.TopicStatus, cfg.NodeID)
	offlinePayload, _ := json.Marshal(wire.NodeStatus{NodeID: cfg.NodeID, Status: wire.NodeStatusOffline})

	opts, err := shared.MQTTClientOptionsFromEnv(cfg.MQTTBroker, "pier-agent-"+cfg.NodeID)
	if err != nil {
		return fmt.Errorf("build MQTT options: %w", err)
	}
	// Retained LWT + retained on-connect/on-shutdown publish, both ways -
	// #459 documented the old client's presence semantics as inconsistent
	// (retained will, non-retained online/offline). pier-agent is the one
	// new implementation in this tree that can just do it right.
	opts.SetWill(statusTopic, string(offlinePayload), 1, true)

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("connect MQTT: %w", token.Error())
	}
	defer client.Disconnect(250)

	if agent.PierCode != nil {
		// A board_post from on-pier code is an opaque message on the campaign
		// stream. Retention seeds late subscribers; active subscribers receive
		// every publish.
		agent.PierCode.PostBoardNote = func(campaignID, nodeID string, note wire.BoardNote) error {
			if note.MessageID == "" {
				note.MessageID = wire.NewMessageID()
			}
			return publishRetained(client, fmt.Sprintf(wire.TopicCampaignBoard, campaignID, nodeID), note)
		}
	}

	// Live campaign-signal + presence/metadata mirror, so on-pier code gets
	// real board_read / list_piers batches. Best-effort: a subscribe failure
	// leaves the watch empty, it does not stop the agent.
	bw := newBoardWatch()
	if err := bw.subscribe(client, 5*time.Second); err != nil {
		log.Printf("pier-agent: board watch subscribe failed, on-pier code will see empty board/pier snapshots: %v", err)
	}

	if err := publishRetained(client, statusTopic, wire.NodeStatus{NodeID: cfg.NodeID, Status: wire.NodeStatusOnline}); err != nil {
		return fmt.Errorf("publish online status: %w", err)
	}
	if err := publishRetained(client, fmt.Sprintf(wire.TopicMetadata, cfg.NodeID), buildNodeMetadata(cfg)); err != nil {
		return fmt.Errorf("publish node metadata: %w", err)
	}

	commandsTopic := fmt.Sprintf(wire.TopicCommands, cfg.NodeID)
	secret := wire.MQTTCommandHMACSecret()
	if secret == "" {
		log.Printf("pier-agent: WARNING - MQTT_COMMAND_HMAC_SECRET is unset; every command will be rejected as unverifiable")
	}
	if token := client.Subscribe(commandsTopic, 1, func(_ mqtt.Client, msg mqtt.Message) {
		handleCommandMessage(agent, bw, secret, cfg.NodeID, msg.Payload())
	}); token.Wait() && token.Error() != nil {
		return fmt.Errorf("subscribe %s: %w", commandsTopic, token.Error())
	}

	log.Printf("pier-agent: online as %s, alpaca=%s, capture_dir=%s", cfg.NodeID, cfg.AlpacaBaseURL, cfg.CaptureDir)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(cfg.TelemetryPeriod)
	defer ticker.Stop()
	telemetryTopic := fmt.Sprintf(wire.TopicTelemetry, cfg.NodeID)

	for {
		select {
		case <-ticker.C:
			publishTelemetry(client, telemetryTopic, cfg.NodeID, agent, tel, cam)
		case <-stop:
			log.Printf("pier-agent: shutting down")
			if err := publishRetained(client, statusTopic, wire.NodeStatus{NodeID: cfg.NodeID, Status: wire.NodeStatusOffline}); err != nil {
				log.Printf("pier-agent: publish offline status on shutdown: %v", err)
			}
			return nil
		}
	}
}

func filterWheelOrNil(fw *alpaca.FilterWheel, connected bool) *alpaca.FilterWheel {
	if !connected {
		return nil
	}
	return fw
}

func buildNodeMetadata(cfg *Config) wire.NodeMetadata {
	return wire.NodeMetadata{
		NodeID:            cfg.NodeID,
		QualityTier:       cfg.Safety.QualityTier,
		SiteLat:           cfg.Safety.SiteLat,
		SiteLon:           cfg.Safety.SiteLon,
		MountLimits:       cfg.Safety.MountLimits,
		HorizonProfile:    cfg.Safety.HorizonProfile,
		ObstructionMask:   cfg.Safety.ObstructionMask,
		ReliabilityScore:  1.0,
		Power:             1.0,
		LimitingMagnitude: cfg.Safety.LimitingMagnitude,
	}
}

func publishRetained(client mqtt.Client, topic string, payload interface{}) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload for %s: %w", topic, err)
	}
	token := client.Publish(topic, 1, true, raw)
	token.Wait()
	return token.Error()
}

func publishTelemetry(client mqtt.Client, topic, nodeID string, agent *Agent, tel telescopeDevice, cam cameraDevice) {
	fw := agent.FilterWheel
	status := "idle"
	if slewing, err := tel.Slewing(); err == nil {
		if state, err := cam.CameraState(); err == nil {
			status = cameraStateToStatus(slewing, state)
		}
	}
	var fwPos func() (int, error)
	if fw != nil {
		fwPos = func() (int, error) {
			p, ok := fw.(interface{ Position() (int, error) })
			if !ok {
				return 0, fmt.Errorf("filter wheel does not expose Position()")
			}
			return p.Position()
		}
	}
	t := buildTelemetry(nodeID, status, tel, cam, fw, fwPos)
	// current_task_id / current_task_priority ride the heartbeat as a matched
	// pair (#404): the collector's lease renewer keys off the id, and the
	// orchestrator's preemption scoring reads the priority — a nil priority
	// beside a non-nil id would blind the latter.
	if id, prio := agent.CurrentTask(); id != nil {
		t.CurrentTaskID = id
		t.CurrentTaskPriority = prio
	}
	raw, err := json.Marshal(t)
	if err != nil {
		log.Printf("pier-agent: marshal telemetry: %v", err)
		return
	}
	token := client.Publish(topic, 0, false, raw)
	token.Wait()
	if err := token.Error(); err != nil {
		log.Printf("pier-agent: publish telemetry: %v", err)
	}
}

// handleCommandMessage verifies the HMAC signature and freshness of an
// incoming command before dispatching - a command that fails either check
// is logged and dropped, never executed. This is the one place in
// pier-agent that trusts wire.Command.Payload's generic interface{} shape;
// everywhere else works with the concrete Assign/PreemptTaskPayload types.
func handleCommandMessage(agent *Agent, bw *boardWatch, secret, nodeID string, raw []byte) {
	var cmd wire.Command
	if err := json.Unmarshal(raw, &cmd); err != nil {
		log.Printf("pier-agent: command decode failed: %v", err)
		return
	}
	if cmd.NodeID != "" && cmd.NodeID != nodeID {
		log.Printf("pier-agent: command node_id %q does not match this node %q, dropping", cmd.NodeID, nodeID)
		return
	}

	payloadJSON, err := json.Marshal(cmd.Payload)
	if err != nil {
		log.Printf("pier-agent: re-marshal command payload failed: %v", err)
		return
	}
	if err := wire.VerifyCommandSignature(secret, cmd.Type, nodeID, cmd.SentAt, cmd.Sig, payloadJSON); err != nil {
		log.Printf("pier-agent: command signature check failed, dropping: %v", err)
		return
	}
	if sentAt, err := time.Parse(time.RFC3339, cmd.SentAt); err != nil || time.Since(sentAt) > wire.CommandMaxAge {
		log.Printf("pier-agent: command sent_at %q is stale or unparseable, dropping", cmd.SentAt)
		return
	}

	switch cmd.Type {
	case "assign_task":
		var payload wire.AssignTaskPayload
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			log.Printf("pier-agent: decode assign_task payload: %v", err)
			return
		}
		agent.prepareNewAssignment()
		path, err := agent.ExecuteAssignTask(payload)
		if err != nil {
			log.Printf("pier-agent: assign_task %d failed: %v", payload.TaskID, err)
			return
		}
		log.Printf("pier-agent: assign_task %d captured -> %s", payload.TaskID, path)
		processCapturedTask(agent, bw, payload, path)
	case "preempt_task":
		var payload wire.PreemptTaskPayload
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			log.Printf("pier-agent: decode preempt_task payload: %v", err)
			return
		}
		path, err := agent.HandlePreemptTask(payload)
		if err != nil {
			log.Printf("pier-agent: preempt_task (prev=%d, new=%d) failed: %v", payload.PrevTaskID, payload.NewTask.TaskID, err)
			return
		}
		log.Printf("pier-agent: preempt_task -> new task %d captured -> %s", payload.NewTask.TaskID, path)
		processCapturedTask(agent, bw, payload.NewTask, path)
	case "abort_task":
		if err := agent.AbortTask(); err != nil {
			log.Printf("pier-agent: abort_task failed: %v", err)
		}
	case "ping":
		// No-op: presence is already covered by the telemetry heartbeat
		// and retained status topic.
	default:
		log.Printf("pier-agent: unhandled command type %q", cmd.Type)
	}
}

// processCapturedTask is shared by normal and preempting assignments so a
// replacement task receives the same upload and on-pier-code processing as an
// ordinary assignment.
func processCapturedTask(agent *Agent, bw *boardWatch, payload wire.AssignTaskPayload, path string) {
	if agent.Uploader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		remotePath, uploadErr := agent.Uploader.Upload(ctx, path, payload)
		cancel()
		if uploadErr != nil {
			log.Printf("pier-agent: task %d upload failed: %v", payload.TaskID, uploadErr)
		} else {
			log.Printf("pier-agent: task %d uploaded -> %s", payload.TaskID, remotePath)
		}
	}
	if agent.PierCode == nil || payload.PierCode == nil {
		return
	}
	var board []wire.BoardNote
	var piers []pierjob.PierSummary
	if bw != nil {
		board = bw.drainBoardSnapshot(payload.CampaignID)
		piers = bw.pierRoster(payload.CampaignID, agent.NodeID)
	}
	if err := agent.PierCode.run(context.Background(), agent.NodeID, payload, path, board, piers); err != nil {
		log.Printf("pier-agent: task %d on-pier code: %v", payload.TaskID, err)
	}
}
