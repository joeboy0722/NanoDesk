package engine

import (
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/webrtc/v4"
	"kvm/core/streamtrace"
)

func videoMediaStack() (*webrtc.MediaEngine, *interceptor.Registry, error) {
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		return nil, nil, err
	}
	registry := &interceptor.Registry{}
	// Desktop frames expire quickly. Unlimited NACK retries can keep an old
	// frame alive indefinitely and starve fresh video on a paced relay path.
	if err := webrtc.RegisterDefaultInterceptorsWithOptions(media, registry,
		webrtc.WithNackGeneratorOptions(nack.GeneratorMaxNacksPerPacket(3))); err != nil {
		return nil, nil, err
	}
	if streamtrace.Enabled() {
		registry.Add(traceFactory{})
	}
	return media, registry, nil
}

func videoAPI(settings webrtc.SettingEngine) (*webrtc.API, error) {
	media, registry, err := videoMediaStack()
	if err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithSettingEngine(settings), webrtc.WithMediaEngine(media), webrtc.WithInterceptorRegistry(registry)), nil
}
