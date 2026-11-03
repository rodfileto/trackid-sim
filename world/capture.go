package world

import "math/rand/v2"

// Capture is how a facial evidence frame was taken: a quality level and every
// degradation parameter it implies. The media package renders from these, so
// world.json holds all a frame depends on besides the pool images and the seed.
// The parameter names follow tools/facegen/degrade.py so the two stay comparable.
type Capture struct {
	Level  string  `json:"level"`
	FacePx float64 `json:"facePx"` // target height of the detected face box in the frame
	Blur   float64 `json:"blur"`   // Gaussian sigma, px
	Noise  float64 `json:"noise"`  // Gaussian sigma, 0-255 levels
	JPEG   int     `json:"jpeg"`   // quality
}

// CaptureLevels are degrade.py's good and medium. The variants' own faces are
// 139-434 px tall (median 224), so both scale down; neither is stacked on top of
// an already small face. degrade.py's poor (48 px) is left out: at that size the
// frame is mostly empty wall, and the evidence is there to be shown.
var CaptureLevels = map[string]Capture{
	"good":   {Level: "good", FacePx: 200, Blur: 0.6, Noise: 3, JPEG: 85},
	"medium": {Level: "medium", FacePx: 100, Blur: 1.2, Noise: 6, JPEG: 60},
}

// assignCaptures gives every facial evidence its capture level. It draws from
// its own stream, so adding it left every other part of the world unchanged.
func assignCaptures(cfg Config, cases []Case) {
	r := rand.New(rand.NewPCG(cfg.Seed, 0xca97))
	for i := range cases {
		if cases[i].Modality != "FACIAL" {
			continue
		}
		for j := range cases[i].Evidences {
			level := "medium"
			if r.Float64() < cfg.GoodCaptureRate {
				level = "good"
			}
			c := CaptureLevels[level]
			cases[i].Evidences[j].Capture = &c
		}
	}
}
