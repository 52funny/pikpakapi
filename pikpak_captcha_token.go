package pikpakapi

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"net/http"
	"time"

	jsoniter "github.com/json-iterator/go"
	"github.com/tidwall/gjson"
)

const package_name = `com.pikcloud.pikpak`
const client_version = `1.21.0`
const md5_obj = `[{"alg":"md5","salt":""},{"alg":"md5","salt":"E32cSkYXC2bciKJGxRsE8ZgwmH\/YwkvpD6\/O9guSOa2irCwciH4xPHaH"},{"alg":"md5","salt":"QtqgfMgHP2TFl"},{"alg":"md5","salt":"zOKgHT56L7nIzFzDpUGhpWFrgP53m3G6ML"},{"alg":"md5","salt":"S"},{"alg":"md5","salt":"THxpsktzfFXizUv7DK1y\/N7NZ1WhayViluBEvAJJ8bA1Wr6"},{"alg":"md5","salt":"y9PXH3xGUhG\/zQI8CaapRw2LhldCaFM9CRlKpZXJvj+pifu"},{"alg":"md5","salt":"+RaaG7T8FRTI4cP019N5y9ofLyHE9ySFUr"},{"alg":"md5","salt":"6Pf1l8UTeuzYldGtb\/d"}]`

type md5Obj struct {
	Alg  string `json:"alg"`
	Salt string `json:"salt"`
}

var md5Arr []md5Obj

func init() {
	err := jsoniter.Unmarshal([]byte(md5_obj), &md5Arr)
	if err != nil {
		panic(fmt.Sprintf("Unmarshal md5_obj error: %v", err))
	}
}

// METHOD:PATH
// eg: POST:/v1/shield/captcha/init
func (p *PikPak) AuthCaptchaToken(action string) error {
	m := make(map[string]interface{})
	m["action"] = action
	m["client_id"] = clientID
	m["device_id"] = p.DeviceId
	if p.CaptchaToken != "" {
		m["captcha_token"] = p.CaptchaToken
	}
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	str := clientID + client_version + package_name + p.DeviceId + ts

	for i := 0; i < len(md5Arr); i++ {
		alg := md5Arr[i].Alg
		salt := md5Arr[i].Salt
		if alg == "md5" {
			str = fmt.Sprintf("%x", md5.Sum([]byte(str+salt)))
		}
	}
	m["meta"] = map[string]string{
		"captcha_sign":   "1." + str,
		"user_id":        p.Sub,
		"package_name":   package_name,
		"client_version": client_version,
		"timestamp":      ts,
	}
	m["redirect_uri"] = "https://api.mypikpak.com/v1/auth/callback"
	bs, err := jsoniter.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", "https://user.mypikpak.com/v1/shield/captcha/init?client_id="+clientID, bytes.NewBuffer(bs))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	bs, err = p.send(req)
	if err != nil {
		return err
	}
	errorCode := gjson.GetBytes(bs, "error_code").Int()
	if errorCode != 0 {
		errorMessage := gjson.GetBytes(bs, "error").String()
		return fmt.Errorf("url: %s error_code: %d, error: %s", req.URL.String(), errorCode, errorMessage)
	}
	p.CaptchaToken = gjson.GetBytes(bs, "captcha_token").String()
	return nil
}
