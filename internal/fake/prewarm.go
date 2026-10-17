package fake

import "sync"

// demoMedia lists the procedural images the demo seed attaches to messages,
// link previews and stories. Rendering and blurhashing them one after the
// other was most of the demo's startup time; prewarmDemoMedia renders them on
// all cores while the seed runs, and the seed's own calls then find them
// memoised (or wait for the render already in flight). The list only
// affects speed: an image missing here is rendered by the seed as before.
// TestDemoMediaPrewarmed keeps it in sync with demo.go.
var demoMedia = []func() []byte{
	signalFireGIF,
	func() []byte { return landscapePNG(640, 360, sceneSunrise) },
	func() []byte { return landscapePNG(320, 480, sceneNoon) },
	func() []byte { return landscapePNG(480, 320, sceneLake) },
	func() []byte { return landscapePNG(400, 400, sceneNoon) },
	func() []byte { return landscapePNG(320, 240, sceneDusk) },
	func() []byte { return landscapePNG(480, 320, sceneSunrise) },
	func() []byte { return landscapePNG(300, 160, sceneDusk) },
	func() []byte { return landscapePNG(360, 270, sceneNoon) },
	func() []byte { return landscapePNG(480, 320, sceneDusk) },
	func() []byte { return landscapePNG(320, 240, sceneLake) },
	func() []byte { return landscapePNG(320, 480, sceneNight) },
	func() []byte { return landscapePNG(400, 260, sceneDusk) },
	func() []byte { return landscapePNG(640, 240, sceneNight) },
	func() []byte { return landscapePNG(270, 480, sceneSunrise) },
	func() []byte { return landscapePNG(270, 480, sceneLake) },
	func() []byte { return patternPNG(240, 160, patternWaves, 0x9fd8a0, 0x2e6b3f) },
	func() []byte { return patternPNG(480, 200, patternWaves, 0xe9f5db, 0x386641) },
	func() []byte { return patternPNG(300, 420, patternStripes, 0xf2e8cf, 0x6a994e) },
	func() []byte { return patternPNG(400, 300, patternCircles, 0xffe5ec, 0xfb6f92) },
	func() []byte { return patternPNG(320, 320, patternWaves, 0xfff3b0, 0xe09f3e) },
	func() []byte { return patternPNG(240, 120, patternChecker, 0xf6f8fa, 0xd0d7de) },
	func() []byte { return patternPNG(240, 320, patternCircles, 0xfaf3e0, 0xc9ada7) },
	func() []byte { return patternPNG(400, 300, patternWaves, 0xe9c46a, 0x8a5a44) },
	func() []byte { return patternPNG(270, 480, patternStripes, 0xf2e8cf, 0x6a994e) },
}

var prewarmOnce sync.Once

// prewarmDemoMedia starts rendering demoMedia (and their blurhashes) in the
// background, once per process. It does not wait: memo makes the seed block
// only on an image it needs that is still being rendered.
func prewarmDemoMedia() {
	prewarmOnce.Do(func() {
		for _, gen := range demoMedia {
			go func() { blurHash(gen()) }()
		}
	})
}
