package gatewaycontract_test

import (
	"strings"
	"testing"

	gatewayv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/gateway/v1"
	publicv1 "github.com/ramaadi/quick-whatsapp-gateway/gen/public/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestGatewayControlServiceDescriptor(t *testing.T) {
	service := gatewayv1.File_v1_gateway_control_proto.Services().ByName("GatewayControlService")
	if service == nil || service.Methods().Len() != 1 {
		t.Fatalf("control service = %v", service)
	}
	connect := service.Methods().ByName("Connect")
	if connect == nil || !connect.IsStreamingClient() || !connect.IsStreamingServer() || connect.Input().Name() != "GatewayFrame" || connect.Output().Name() != "ControlFrame" {
		t.Fatalf("Connect descriptor = %v", connect)
	}
	if publicv1.File_v1_public_health_proto.Services().ByName("GatewayControlService") != nil {
		t.Fatal("private control service leaked into public module")
	}
}

func TestControlFrameWireNumbersAndOneofs(t *testing.T) {
	tests := []struct {
		message protoreflect.MessageDescriptor
		fields  map[protoreflect.Name]protoreflect.FieldNumber
		oneof   []protoreflect.Name
	}{
		{gatewayv1.File_v1_gateway_control_proto.Messages().ByName("GatewayFrame"), map[protoreflect.Name]protoreflect.FieldNumber{"protocol_version": 1, "sequence": 2, "hello": 10, "heartbeat": 11, "lifecycle_report": 12}, []protoreflect.Name{"hello", "heartbeat", "lifecycle_report"}},
		{gatewayv1.File_v1_gateway_control_proto.Messages().ByName("ControlFrame"), map[protoreflect.Name]protoreflect.FieldNumber{"protocol_version": 1, "sequence": 2, "welcome": 10, "lifecycle_directive": 11}, []protoreflect.Name{"welcome", "lifecycle_directive"}},
	}
	for _, tt := range tests {
		if tt.message == nil || tt.message.Fields().ByName("gateway_id") != nil || tt.message.Oneofs().Len() != 1 || tt.message.Oneofs().Get(0).Name() != "payload" {
			t.Fatalf("%v payload oneof changed", tt.message)
		}
		for name, number := range tt.fields {
			field := tt.message.Fields().ByName(name)
			if field == nil || field.Number() != number {
				t.Fatalf("%s.%s number = %v, want %d", tt.message.Name(), name, field, number)
			}
		}
		for _, name := range tt.oneof {
			if tt.message.Fields().ByName(name).ContainingOneof() != tt.message.Oneofs().Get(0) {
				t.Fatalf("%s.%s left payload oneof", tt.message.Name(), name)
			}
		}
	}
}

func TestControlMessagesHaveStableFieldsAndNoGatewayID(t *testing.T) {
	want := map[protoreflect.Name]map[protoreflect.Name]protoreflect.FieldNumber{
		"GatewayHello":           {"instance_id": 1, "software_version": 2, "capabilities": 3, "grpc_endpoint": 4, "started_at_unix_ms": 5, "session_count": 6, "runtime_state": 7},
		"GatewayHeartbeat":       {"connection_epoch": 1, "last_control_sequence": 2, "sent_at_unix_ms": 3, "session_count": 4, "runtime_state": 5},
		"GatewayLifecycleReport": {"connection_epoch": 1, "directive_id": 2, "state": 3, "failure": 4},
		"ControlWelcome":         {"connection_id": 1, "connection_epoch": 2, "heartbeat_interval_ms": 3, "lease_timeout_ms": 4, "desired_lifecycle": 5, "server_time_unix_ms": 6},
		"LifecycleDirective":     {"directive_id": 1, "connection_epoch": 2, "action": 3, "drain_deadline_unix_ms": 4, "reason": 5},
	}
	messages := gatewayv1.File_v1_gateway_control_proto.Messages()
	for messageName, fields := range want {
		message := messages.ByName(messageName)
		if message == nil || message.Fields().ByName("gateway_id") != nil {
			t.Fatalf("%s missing or contains gateway_id", messageName)
		}
		for fieldName, number := range fields {
			field := message.Fields().ByName(fieldName)
			if field == nil || field.Number() != number {
				t.Fatalf("%s.%s changed", messageName, fieldName)
			}
		}
	}
	if !messages.ByName("GatewayHello").Fields().ByName("grpc_endpoint").HasOptionalKeyword() || !messages.ByName("LifecycleDirective").Fields().ByName("drain_deadline_unix_ms").HasOptionalKeyword() {
		t.Fatal("optional presence semantics changed")
	}
	for _, enumName := range []protoreflect.Name{"GatewayCapability", "GatewayRuntimeState", "LifecycleFailure", "LifecycleDirectiveAction", "LifecycleDirectiveReason"} {
		enum := gatewayv1.File_v1_gateway_control_proto.Enums().ByName(enumName)
		if enum == nil || enum.Values().Get(0).Number() != 0 || !strings.HasSuffix(string(enum.Values().Get(0).Name()), "_UNKNOWN") {
			t.Fatalf("%s zero value changed", enumName)
		}
	}
}
