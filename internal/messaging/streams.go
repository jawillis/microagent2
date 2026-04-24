package messaging

const (
	StreamGatewayRequests = "stream:gateway:requests"
	StreamRegistryAnnounce = "stream:registry:announce"
	StreamAgentRequests    = "stream:agent:%s:requests"

	ChannelTokens    = "channel:tokens:%s"
	ChannelHeartbeat = "channel:heartbeat:%s"
	ChannelPreempt   = "channel:agent:%s:preempt"
	ChannelEvents    = "channel:events"

	ConsumerGroupGateway        = "cg:gateway"
	ConsumerGroupContextManager = "cg:context-manager"
	ConsumerGroupBroker         = "cg:broker"
	ConsumerGroupAgent          = "cg:agent:%s"
)
