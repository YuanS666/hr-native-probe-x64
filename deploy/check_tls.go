package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"time"
)

// main 校验参数中的证书链、私钥匹配、域名及有效期；失败返回非零退出码，不输出私钥内容。
func main() {
	log.SetFlags(0)
	if len(os.Args) != 4 {
		log.Fatal("用法：check-tls 完整证书链 私钥 域名")
	}
	pair, err := tls.LoadX509KeyPair(os.Args[1], os.Args[2])
	if err != nil {
		log.Fatalf("证书与私钥加载失败：%v", err)
	}
	now := time.Now()
	var previous *x509.Certificate
	for i, der := range pair.Certificate {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			log.Fatal(err)
		}
		if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
			log.Fatalf("第 %d 张证书不在有效期内", i+1)
		}
		if i == 0 {
			if err := cert.VerifyHostname(os.Args[3]); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("域名匹配：%s；服务器证书有效至 %s\n", os.Args[3], cert.NotAfter.Format(time.RFC3339))
		} else if err := previous.CheckSignatureFrom(cert); err != nil {
			log.Fatalf("第 %d 张与前一张证书无法组成有序链：%v", i+1, err)
		}
		previous = cert
	}
	fmt.Printf("私钥匹配及 %d 张证书内部链校验通过；客户端系统根信任仍需 HTTPS 实测确认。\n", len(pair.Certificate))
}
