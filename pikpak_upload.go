package pikpakapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/52funny/pikpakhash"
	jsoniter "github.com/json-iterator/go"
	"github.com/tidwall/gjson"
)

// 256k
var defaultChunkSize int64 = 1 << 18
var Concurrent int64 = 1 << 4

type header struct {
	key string
	val string
}

type CompleteMultipartUpload struct {
	Part []part `xml:"Part"`
}

type part struct {
	PartNumber string `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type ossArgs struct {
	Bucket          string `json:"bucket"`
	AccessKeyId     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
	EndPoint        string `json:"endpoint"`
	Key             string `json:"key"`
	SecurityToken   string `json:"security_token"`
}

func (p *PikPak) UploadFile(parentID, path string) (string, error) {
	fileName := filepath.Base(path)
	fileState, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	fileSize := fileState.Size()
	ph := pikpakhash.Default()
	hash, err := ph.HashFromPath(path)
	if err != nil {
		return "", err
	}
	m := map[string]interface{}{
		"body": map[string]string{
			"duration": "",
			"width":    "",
			"height":   "",
		},
		"kind":        KIND_FILE,
		"name":        fileName,
		"size":        fmt.Sprintf("%d", fileSize),
		"hash":        hash,
		"upload_type": "UPLOAD_TYPE_RESUMABLE",
		"objProvider": map[string]string{
			"provider": "UPLOAD_TYPE_UNKNOWN",
		},
	}
	if parentID != "" {
		// ParentID equals to "" means upload to root directory
		// If parent_id is not present, the file will be uploaded to the "My Upload" folder
		m["parent_id"] = parentID
	}
	bs, err := jsoniter.Marshal(&m)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", "https://api-drive.mypikpak.com/drive/v1/files", bytes.NewBuffer(bs))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Product_flavor_name", "cha")
	req.Header.Set("X-Captcha-Token", p.CaptchaToken)
	req.Header.Set("X-Client-Version-Code", "10083")
	req.Header.Set("X-Peer-Id", p.DeviceId)
	req.Header.Set("X-User-Region", "1")
	req.Header.Set("X-Alt-Capability", "3")
	req.Header.Set("Country", "CN")
	bs, err = p.sendWithErrHandle(req, bs)
	if err != nil {
		return "", err
	}
	file := gjson.GetBytes(bs, "file")
	phase := file.Get("phase").String()
	fileID := file.Get("id").String()
	logger.Debug("UploadFile", "path: ", path, " phase: ", phase)

	switch phase {
	case "PHASE_TYPE_COMPLETE":
		logger.Debug("UploadFile already exists")
		return fileID, nil
	case "PHASE_TYPE_PENDING":
		// break switch
		break
	}
	params := gjson.GetBytes(bs, "resumable.params")

	accessKeyId := params.Get("access_key_id").String()
	accessKeySecret := params.Get("access_key_secret").String()
	bucket := params.Get("bucket").String()
	endpoint := params.Get("endpoint").String()
	key := params.Get("key").String()
	securityToken := params.Get("security_token").String()

	ossArgs := ossArgs{
		Bucket:          bucket,
		AccessKeyId:     accessKeyId,
		AccessKeySecret: accessKeySecret,
		EndPoint:        endpoint,
		Key:             key,
		SecurityToken:   securityToken,
	}

	uploadId := p.beforeUpload(ossArgs)

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	wait := new(sync.WaitGroup)
	in_wait := new(sync.WaitGroup)
	ch := make(chan part, Concurrent)

	var chunkSize = int64(math.Ceil(float64(fileSize) / 10000))
	if chunkSize < defaultChunkSize {
		chunkSize = defaultChunkSize
	}

	for i := int64(0); i < Concurrent; i++ {
		wait.Add(1)
		go uploadChunk(wait, ch, f, chunkSize, fileSize, i, ossArgs, uploadId)
	}
	donePartSlice := make([]part, 0)
	in_wait.Add(1)
	go func() {
		defer in_wait.Done()
		for p := range ch {
			donePartSlice = append(donePartSlice, p)
		}
	}()
	wait.Wait()
	close(ch)
	in_wait.Wait()
	sort.Slice(donePartSlice, func(i, j int) bool {
		iNum, _ := strconv.Atoi(donePartSlice[i].PartNumber)
		jNum, _ := strconv.Atoi(donePartSlice[j].PartNumber)
		return iNum < jNum
	})
	args := CompleteMultipartUpload{
		Part: donePartSlice,
	}
	err = p.afterUpload(&args, ossArgs, uploadId)
	if err != nil {
		return "", err
	}
	return fileID, nil
}

func uploadChunk(wait *sync.WaitGroup, ch chan part, f *os.File, ChunkSize, fileSize int64, partSize int64, ossArgs ossArgs, uploadId string) {
	defer wait.Done()
	if partSize*ChunkSize >= fileSize {
		return
	}
	buf := make([]byte, ChunkSize)
	var offset = partSize * ChunkSize
	for offset < fileSize {
		n, _ := f.ReadAt(buf, offset)
		if n > 0 {
			value := url.Values{}
			value.Add("uploadId", uploadId)
			value.Add("partNumber", fmt.Sprintf("%d", partSize+1))
			req, err := http.NewRequest("PUT", fmt.Sprintf("https://%s/%s?%s",
				ossArgs.EndPoint,
				ossArgs.Key,
				value.Encode()), bytes.NewBuffer(buf[:n]))
			if err != nil {
				continue
			}

			now := time.Now().UTC()
			req.Header.Set("Content-Type", "application/octet-stream")
			req.Header.Set("X-OSS-Security-Token", ossArgs.SecurityToken)
			req.Header.Set("Date", now.Format(http.TimeFormat))
			req.Header.Set("Authorization", "OSS "+ossArgs.AccessKeyId+":"+hmacAuthorization(req, nil, now, ossArgs))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				continue
			}
			// bs, _ := io.ReadAll(resp.Body)
			eTag := strings.Trim(resp.Header.Get("ETag"), "\"")
			p := part{
				PartNumber: fmt.Sprintf("%d", partSize+1),
				ETag:       eTag,
			}
			ch <- p
			resp.Body.Close()
		}
		partSize = partSize + Concurrent
		offset = partSize * ChunkSize
	}
}

func hmacAuthorization(req *http.Request, body []byte, time time.Time, ossArgs ossArgs) string {
	date := time.UTC().Format(http.TimeFormat)
	stringBuilder := new(strings.Builder)
	stringBuilder.WriteString(req.Method + "\n")
	if body == nil {
		stringBuilder.WriteString("\n")
	} else {
		// digest := md5.New()
		// digest.Write(body)
		// sign := base64.StdEncoding.EncodeToString(digest.Sum(nil))
		// stringBuilder.WriteString(sign + "\n")
		stringBuilder.WriteString("\n")
	}
	stringBuilder.WriteString(req.Header.Get("Content-Type") + "\n")
	stringBuilder.WriteString(date + "\n")

	headerSlice := make([]header, 0)
	for k, v := range req.Header {
		headerK := strings.ToLower(k)
		if strings.Contains(headerK, "x-oss-") && len(v) > 0 {
			headerSlice = append(headerSlice, header{headerK, v[0]})
		}
	}

	// 从小到大排序
	sort.Slice(headerSlice, func(i, j int) bool {
		return headerSlice[i].key < headerSlice[j].key
	})
	for _, hd := range headerSlice {
		stringBuilder.WriteString(hd.key + ":" + hd.val + "\n")
	}

	stringBuilder.WriteString("/" + ossArgs.Bucket + req.URL.Path + "?" + req.URL.RawQuery)

	h := hmac.New(sha1.New, []byte(ossArgs.AccessKeySecret))
	h.Write([]byte(stringBuilder.String()))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (p *PikPak) beforeUpload(ossArgs ossArgs) string {
	req, err := http.NewRequest("POST", "https://"+ossArgs.EndPoint+"/"+ossArgs.Key+"?uploads", nil)
	if err != nil {
		return ""
	}
	time := time.Now().UTC()
	req.Header.Set("Date", time.Format(http.TimeFormat))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "aliyun-sdk-android/2.9.5(Linux/Android 11/ONEPLUS%20A6000;RKQ1.201217.002)")
	req.Header.Set("X-Oss-Security-Token", ossArgs.SecurityToken)
	req.Header.Set("Authorization",
		fmt.Sprintf("%s %s:%s",
			"OSS",
			ossArgs.AccessKeyId,
			hmacAuthorization(req, nil, time, ossArgs),
		))

	resp, err := p.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	bs, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	type InitiateMultipartUploadResult struct {
		Bucket   string `xml:"Bucket"`
		Key      string `xml:"Key"`
		UploadId string `xml:"UploadId"`
	}
	res := new(InitiateMultipartUploadResult)

	err = xml.Unmarshal(bs, res)
	if err != nil {
		return ""
	}
	return res.UploadId
}

func (p *PikPak) afterUpload(args *CompleteMultipartUpload, ossArgs ossArgs, uploadId string) error {
	bs, err := xml.Marshal(args)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", "https://"+ossArgs.EndPoint+"/"+ossArgs.Key+"?uploadId="+uploadId, bytes.NewBuffer(bs))
	if err != nil {
		return err
	}
	time := time.Now().UTC()
	req.Header.Set("Date", time.Format(http.TimeFormat))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "aliyun-sdk-android/2.9.5(Linux/Android 11/ONEPLUS%20A6000;RKQ1.201217.002)")
	req.Header.Set("X-Oss-Security-Token", ossArgs.SecurityToken)
	req.Header.Set("Authorization",
		fmt.Sprintf("%s %s:%s",
			"OSS",
			ossArgs.AccessKeyId,
			hmacAuthorization(req, nil, time, ossArgs),
		))

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return nil
}
