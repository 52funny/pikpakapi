package pikpakapi

import (
	"bytes"
	"fmt"
	"net/http"

	jsoniter "github.com/json-iterator/go"
	"github.com/tidwall/gjson"
)

func (p *PikPak) RefreshToken() error {
	url := "https://user.mypikpak.com/v1/auth/token"
	m := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"grant_type":    "refresh_token",
		"refresh_token": p.refreshToken,
	}
	bs, err := jsoniter.Marshal(&m)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(bs))
	if err != nil {
		return err
	}
	bs, err = p.sendRequest(req)
	if err != nil {
		return err
	}
	error_code := gjson.GetBytes(bs, "error_code").Int()
	if error_code != 0 {
		// refresh token failed
		if error_code == 4126 {
			// 重新登录
			return p.Login()
		}
		return fmt.Errorf("refresh token error message: %d", gjson.GetBytes(bs, "error").Int())
	}
	p.JwtToken = gjson.GetBytes(bs, "access_token").String()
	p.refreshToken = gjson.GetBytes(bs, "refresh_token").String()
	p.RefreshSecond = gjson.GetBytes(bs, "expires_in").Int()
	logger.Debug("RefreshToken", "access_token", p.JwtToken, "refresh_token", p.refreshToken)
	return nil
}
