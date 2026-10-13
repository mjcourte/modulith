package probe

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestToInfo(t *testing.T) {
	tests := []struct {
		name string // also the fixture: testdata/<name>.json
		want Info
	}{
		{
			name: "phone_video_portrait",
			want: Info{
				Duration:      2837292 * time.Microsecond, // format duration; longer than the video's 2.699s
				DisplayWidth:  1080,                       // stored 1920x1080, rotated -90
				DisplayHeight: 1920,
				BitRate:       12967085,
				Format:        "mov,mp4,m4a,3gp,3g2,mj2",
				Video: []VideoStream{{
					FrameRate: 455625.0 / 15184, // average rate, a phone's variable ~30fps
					Width:     1920,
					Height:    1080,
					Rotation:  -90,
					Codec:     "hevc",
					BitRate:   13348874,
				}},
				Audio: []AudioStream{{
					SampleRate: 48000,
					Channels:   2,
					Codec:      "aac",
					BitRate:    256540,
				}},
			},
		},
		{
			name: "phone_video_landscape",
			want: Info{
				Duration:      1855979 * time.Microsecond,
				DisplayWidth:  1920, // no side data: shown as stored
				DisplayHeight: 1080,
				BitRate:       14642544,
				Format:        "mov,mp4,m4a,3gp,3g2,mj2",
				Video: []VideoStream{{
					FrameRate: 918000.0 / 30443,
					Width:     1920,
					Height:    1080,
					Codec:     "hevc",
					BitRate:   15772415,
				}},
				Audio: []AudioStream{{
					SampleRate: 48000,
					Channels:   2,
					Codec:      "aac",
					BitRate:    256765,
				}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + tc.name + ".json")
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var pr probeResult
			if err := json.Unmarshal(data, &pr); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			if got := pr.toInfo(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("toInfo:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
