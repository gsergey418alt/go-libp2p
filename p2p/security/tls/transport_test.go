package libp2ptls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	mrand "math/rand"
	"net"

	cs "github.com/libp2p/go-conn-security"
	ic "github.com/libp2p/go-libp2p-crypto"
	peer "github.com/libp2p/go-libp2p-peer"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Transport", func() {
	var (
		serverKey, clientKey ic.PrivKey
		serverID, clientID   peer.ID
	)

	createPeer := func() (peer.ID, ic.PrivKey) {
		var priv ic.PrivKey
		if mrand.Int()%2 == 0 {
			fmt.Fprintf(GinkgoWriter, " using an ECDSA key: ")
			var err error
			priv, _, err = ic.GenerateECDSAKeyPair(rand.Reader)
			Expect(err).ToNot(HaveOccurred())
		} else {
			fmt.Fprintf(GinkgoWriter, " using an RSA key: ")
			var err error
			priv, _, err = ic.GenerateRSAKeyPair(1024, rand.Reader)
			Expect(err).ToNot(HaveOccurred())
		}
		id, err := peer.IDFromPrivateKey(priv)
		Expect(err).ToNot(HaveOccurred())
		fmt.Fprintln(GinkgoWriter, id.Pretty())
		return id, priv
	}

	connect := func() (net.Conn, net.Conn) {
		ln, err := net.Listen("tcp", "localhost:0")
		Expect(err).ToNot(HaveOccurred())
		defer ln.Close()
		serverConnChan := make(chan net.Conn)
		go func() {
			defer GinkgoRecover()
			conn, err := ln.Accept()
			Expect(err).ToNot(HaveOccurred())
			serverConnChan <- conn
		}()
		conn, err := net.Dial("tcp", ln.Addr().String())
		Expect(err).ToNot(HaveOccurred())
		return conn, <-serverConnChan
	}

	// modify the cert chain such that verificiation will fail
	invalidateCertChain := func(identity *Identity) {
		switch identity.Config.Certificates[0].PrivateKey.(type) {
		case *rsa.PrivateKey:
			key, err := rsa.GenerateKey(rand.Reader, 1024)
			Expect(err).ToNot(HaveOccurred())
			identity.Config.Certificates[0].PrivateKey = key
		case *ecdsa.PrivateKey:
			key, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
			Expect(err).ToNot(HaveOccurred())
			identity.Config.Certificates[0].PrivateKey = key
		default:
			Fail("unexpected private key type")
		}
	}

	BeforeEach(func() {
		fmt.Fprintf(GinkgoWriter, "Initializing a server")
		serverID, serverKey = createPeer()
		fmt.Fprintf(GinkgoWriter, "Initializing a client")
		clientID, clientKey = createPeer()
	})

	It("handshakes", func() {
		clientTransport, err := New(clientKey)
		Expect(err).ToNot(HaveOccurred())
		serverTransport, err := New(serverKey)
		Expect(err).ToNot(HaveOccurred())

		clientInsecureConn, serverInsecureConn := connect()

		serverConnChan := make(chan cs.Conn)
		go func() {
			defer GinkgoRecover()
			serverConn, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn)
			Expect(err).ToNot(HaveOccurred())
			serverConnChan <- serverConn
		}()
		clientConn, err := clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
		Expect(err).ToNot(HaveOccurred())
		var serverConn cs.Conn
		Eventually(serverConnChan).Should(Receive(&serverConn))
		defer clientConn.Close()
		defer serverConn.Close()
		Expect(clientConn.LocalPeer()).To(Equal(clientID))
		Expect(serverConn.LocalPeer()).To(Equal(serverID))
		Expect(clientConn.LocalPrivateKey()).To(Equal(clientKey))
		Expect(serverConn.LocalPrivateKey()).To(Equal(serverKey))
		Expect(clientConn.RemotePeer()).To(Equal(serverID))
		Expect(serverConn.RemotePeer()).To(Equal(clientID))
		Expect(clientConn.RemotePublicKey()).To(Equal(serverKey.GetPublic()))
		Expect(serverConn.RemotePublicKey()).To(Equal(clientKey.GetPublic()))
		// exchange some data
		_, err = serverConn.Write([]byte("foobar"))
		Expect(err).ToNot(HaveOccurred())
		b := make([]byte, 6)
		_, err = clientConn.Read(b)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(b)).To(Equal("foobar"))
	})

	It("fails when the context of the outgoing connection is canceled", func() {
		clientTransport, err := New(clientKey)
		Expect(err).ToNot(HaveOccurred())
		serverTransport, err := New(serverKey)
		Expect(err).ToNot(HaveOccurred())

		clientInsecureConn, serverInsecureConn := connect()

		go func() {
			defer GinkgoRecover()
			_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn)
			Expect(err).To(HaveOccurred())
		}()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = clientTransport.SecureOutbound(ctx, clientInsecureConn, serverID)
		Expect(err).To(MatchError(context.Canceled))
	})

	It("fails when the context of the incoming connection is canceled", func() {
		clientTransport, err := New(clientKey)
		Expect(err).ToNot(HaveOccurred())
		serverTransport, err := New(serverKey)
		Expect(err).ToNot(HaveOccurred())

		clientInsecureConn, serverInsecureConn := connect()

		go func() {
			defer GinkgoRecover()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := serverTransport.SecureInbound(ctx, serverInsecureConn)
			Expect(err).To(MatchError(context.Canceled))
		}()
		_, err = clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
		Expect(err).To(HaveOccurred())
	})

	It("fails if the peer ID doesn't match", func() {
		fmt.Fprintf(GinkgoWriter, "Creating another peer")
		thirdPartyID, _ := createPeer()

		serverTransport, err := New(serverKey)
		Expect(err).ToNot(HaveOccurred())
		clientTransport, err := New(clientKey)
		Expect(err).ToNot(HaveOccurred())

		clientInsecureConn, serverInsecureConn := connect()

		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("tls: bad certificate"))
			close(done)
		}()
		// dial, but expect the wrong peer ID
		_, err = clientTransport.SecureOutbound(context.Background(), clientInsecureConn, thirdPartyID)
		Expect(err).To(MatchError("peer IDs don't match"))
		Eventually(done).Should(BeClosed())
	})

	It("fails if the client presents an invalid cert chain", func() {
		serverTransport, err := New(serverKey)
		Expect(err).ToNot(HaveOccurred())
		clientTransport, err := New(clientKey)
		Expect(err).ToNot(HaveOccurred())
		invalidateCertChain(clientTransport.identity)

		clientInsecureConn, serverInsecureConn := connect()

		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(
				ContainSubstring("crypto/rsa: verification error"),
				ContainSubstring("ECDSA verification failure"),
			))
			close(done)
		}()

		_, err = clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("tls: bad certificate"))
		Eventually(done).Should(BeClosed())
	})

	It("fails if the server presents an invalid cert chain", func() {
		serverTransport, err := New(serverKey)
		Expect(err).ToNot(HaveOccurred())
		invalidateCertChain(serverTransport.identity)
		clientTransport, err := New(clientKey)
		Expect(err).ToNot(HaveOccurred())

		clientInsecureConn, serverInsecureConn := connect()

		done := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			_, err := serverTransport.SecureInbound(context.Background(), serverInsecureConn)
			Expect(err).To(HaveOccurred())
			// TLS returns a weird error here: "remote error: tls: unexpected message"
			close(done)
		}()

		_, err = clientTransport.SecureOutbound(context.Background(), clientInsecureConn, serverID)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(Or(
			ContainSubstring("crypto/rsa: verification error"),
			ContainSubstring("ECDSA verification failure"),
		))
		Eventually(done).Should(BeClosed())
	})
})
