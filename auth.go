package centrifuge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Exp is helper function to get sign exp timestamp as
// string - i.e. in a format Centrifuge server expects.
// Actually in most cases you need an analogue of this
// function on your app backend when generating client
// connection credentials - it's added to this client
// mostly for reference and demo use cases.
func Exp(ttlSeconds int) string {
	return strconv.FormatInt(time.Now().Unix()+int64(ttlSeconds), 10)
}

// GenerateClientSign generates client token based on project secret key and provided
// connection parameters such as user ID, exp and info JSON string.
// Actually in most cases you need an analogue of this
// function on your app backend when generating client
// connection credentials - it's added to this client
// mostly for reference and demo use cases.
func GenerateClientSign(secret, user, exp, info string) string {
	token := hmac.New(sha256.New, []byte(secret))
	token.Write([]byte(user))
	token.Write([]byte(exp))
	token.Write([]byte(info))
	return hex.EncodeToString(token.Sum(nil))
}

// GenerateChannelSign generates sign which is used to prove permission of
// client to subscribe on private channel.
// Actually in most cases you need an analogue of this
// function on your app backend when generating client
// connection credentials - it's added to this client
// mostly for reference and demo use cases.
func GenerateChannelSign(secret, client, channel, channelData string) string {
	sign := hmac.New(sha256.New, []byte(secret))
	sign.Write([]byte(client))
	sign.Write([]byte(channel))
	sign.Write([]byte(channelData))
	return hex.EncodeToString(sign.Sum(nil))
}
