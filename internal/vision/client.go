package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxImageBytes = 8 << 20

// Client 将图片字节作为原生 input_image 内容发送给兼容 Responses API 的视觉模型。
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

func NewClient(apiKey, baseURL, model string) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(model) == "" {
		return nil, errors.New("视觉模型 API Key 和模型名称不能为空")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &Client{
		apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), model: model,
		http: &http.Client{Timeout: 90 * time.Second},
	}, nil
}

// Analyze 将截图转换为 data URL，并放入 input_image 内容项中发送。
func (c *Client) Analyze(ctx context.Context, imageData []byte, mimeType, question string) (string, error) {
	if c == nil {
		return "", errors.New("视觉客户端未初始化")
	}
	if len(imageData) == 0 || len(imageData) > maxImageBytes {
		return "", fmt.Errorf("图片大小必须在 1 字节到 %d 字节之间", maxImageBytes)
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		return "", errors.New("仅支持 PNG、JPEG、WebP 和 GIF 图片")
	}
	if strings.TrimSpace(question) == "" {
		question = "请检查这张网页截图的布局、视觉层级、对齐、间距、字体、颜色、响应式表现和明显 UI 缺陷，并给出具体修改建议。"
	}
	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(imageData)
	payload := map[string]any{
		"model": c.model,
		"input": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": question},
				map[string]any{"type": "input_image", "image_url": dataURL, "detail": "high"},
			},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	endpoint := c.baseURL
	if !strings.HasSuffix(endpoint, "/responses") {
		endpoint += "/responses"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用视觉模型失败: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("视觉模型返回 HTTP %d: %s", resp.StatusCode, truncate(string(responseBody), 1200))
	}
	var result struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("解析视觉模型响应失败: %w", err)
	}
	if strings.TrimSpace(result.OutputText) != "" {
		return strings.TrimSpace(result.OutputText), nil
	}
	var parts []string
	for _, item := range result.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				parts = append(parts, strings.TrimSpace(content.Text))
			}
		}
	}
	if len(parts) == 0 {
		return "", errors.New("视觉模型没有返回可读文本")
	}
	return strings.Join(parts, "\n"), nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}
