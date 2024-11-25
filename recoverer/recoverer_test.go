package recoverer

import (
	"testing"
)

func TestGetBucketAndPrefix(t *testing.T) {
	type testCase struct {
		address        string
		expecteBucket  string
		expectedPrefix string
	}
	cases := []testCase{
		{
			address:        "operator-testing/test",
			expecteBucket:  "operator-testing",
			expectedPrefix: "test/",
		},
		{
			address:        "s3://operator-testing/test",
			expecteBucket:  "operator-testing",
			expectedPrefix: "test/",
		},
		{
			address:        "https://somedomain/operator-testing/test",
			expecteBucket:  "operator-testing",
			expectedPrefix: "test/",
		},
		{
			address:        "operator-testing/test/",
			expecteBucket:  "operator-testing",
			expectedPrefix: "test/",
		},
		{
			address:        "operator-testing/test/pitr",
			expecteBucket:  "operator-testing",
			expectedPrefix: "test/pitr/",
		},
		{
			address:        "https://somedomain/operator-testing",
			expecteBucket:  "operator-testing",
			expectedPrefix: "",
		},
		{
			address:        "operator-testing",
			expecteBucket:  "operator-testing",
			expectedPrefix: "",
		},
	}
	for _, c := range cases {
		t.Run(c.address, func(t *testing.T) {
			bucket, prefix, err := getBucketAndPrefix(c.address)
			if err != nil {
				t.Errorf("get from '%s': %s", c.address, err.Error())
			}
			if bucket != c.expecteBucket || prefix != c.expectedPrefix {
				t.Errorf("%s: bucket expect '%s', got '%s'; prefix expect '%s', got '%s'", c.address, c.expecteBucket, bucket, c.expectedPrefix, prefix)
			}
		})
	}
}
