package messaging

const (
	StreamGatewayRequests = "stream:gateway:requests"
	StreamRegistryAnnounce = "stream:registry:announce"
	StreamAgentRequests    = "stream:agent:%s:requests"

	ChannelTokens    = "channel:tokens:%s"
	ChannelToolCalls = "channel:tool-calls:%s"
	ChannelHeartbeat = "channel:heartbeat:%s"
	ChannelPreempt   = "channel:agent:%s:preempt"
	ChannelEvents    = "channel:events"

	ConsumerGroupGateway        = "cg:gateway"
	ConsumerGroupContextManager = "cg:context-manager"
	ConsumerGroupBroker         = "cg:broker"
	ConsumerGroupAgent          = "cg:agent:%s"

	StreamRetroTriggers = "stream:retro:triggers"
	ConsumerGroupRetro  = "cg:retro"

	StreamBrokerSlotSnapshot    = "stream:broker:slot-snapshot-requests"
	ConsumerGroupBrokerSnapshot = "cg:broker:snapshot"
)
