package echo

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestDescriptorSetRoundTrip(t *testing.T) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(DescriptorSet(), &set); err != nil {
		t.Fatal(err)
	}
	if len(set.File) != 1 || set.File[0].GetPackage() != "verity.testpb" {
		t.Fatalf("set: %+v", set.File)
	}
}
