// Separate module: facescore needs cgo + onnxruntime, and the simulator's own
// build must not inherit that. See main.go.
module github.com/rodfileto/trackid-sim/tools/facescore

go 1.27.1

require github.com/rodfileto/trackid-vision v0.2.0

require (
	github.com/yalue/onnxruntime_go v1.24.0 // indirect
	golang.org/x/image v0.46.0 // indirect
)

replace github.com/rodfileto/trackid-vision => ../../../trackid-vision
