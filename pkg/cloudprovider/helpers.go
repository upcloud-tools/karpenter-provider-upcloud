package cloudprovider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getCABundle(ctx context.Context, c client.Client) (string, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      "kube-root-ca.crt",
		Namespace: metav1.NamespaceSystem,
	}, cm); err != nil {
		return "", fmt.Errorf("getting kube-root-ca.crt: %w", err)
	}

	caCert, ok := cm.Data["ca.crt"]
	if !ok {
		return "", fmt.Errorf("ca.crt not found in kube-root-ca.crt")
	}

	return caCert, nil
}

type kubeletCertBundle struct {
	ClientCertPEM string
	ClientKeyPEM  string
}

func generateKubeletCert(ctx context.Context, kubeClient kubernetes.Interface, nodeName string) (*kubeletCertBundle, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating RSA key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("system:node:%s", nodeName),
			Organization: []string{"system:nodes"},
		},
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, fmt.Errorf("creating CSR: %w", err)
	}

	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})

	csrName := fmt.Sprintf("kubelet-%s", nodeName)

	csr := &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name: csrName,
		},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    csrPEM,
			SignerName: certificatesv1.KubeAPIServerClientKubeletSignerName,
			Usages: []certificatesv1.KeyUsage{
				certificatesv1.UsageDigitalSignature,
				certificatesv1.UsageKeyEncipherment,
				certificatesv1.UsageClientAuth,
			},
		},
	}

	if _, err := kubeClient.CertificatesV1().CertificateSigningRequests().Create(ctx, csr, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("creating CSR: %w", err)
	}

	// Re-fetch the CSR to have a clean server-side copy before approving.
	// The object returned by Create may have metadata that confuses the approval subresource on some server versions.
	fresh, err := kubeClient.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting CSR for approval: %w", err)
	}
	// If an external controller (e.g. kube-controller-manager's csrapproving) already acted before we got here, 
	// respect the existing decision.
	alreadyApproved := false
	alreadyDenied := false
	for _, c := range fresh.Status.Conditions {
		if c.Type == certificatesv1.CertificateApproved {
			alreadyApproved = true
		}
		if c.Type == certificatesv1.CertificateDenied {
			alreadyDenied = true
		}
	}
	if alreadyDenied {
		return nil, fmt.Errorf("CSR %s was denied", csrName)
	}
	if !alreadyApproved {
		// Approve the CSR ourselves since the operator's SA is not in the bootstrappers group.
		fresh.Status.Conditions = append(fresh.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
			Type:               certificatesv1.CertificateApproved,
			Status:             corev1.ConditionTrue,
			Reason:             "KarpenterAutoApproved",
			Message:            "Auto-approved by Karpenter UpCloud provider",
			LastUpdateTime:     metav1.Now(),
			LastTransitionTime: metav1.Now(),
		})
		if _, err := kubeClient.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csrName, fresh, metav1.UpdateOptions{}); err != nil {
			return nil, fmt.Errorf("approving CSR: %w", err)
		}
	}

	var signedCert []byte
	// Use a detached context so the wait isn't bounded by the controller's short reconciliation deadline.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer waitCancel()
	pollErr := wait.PollUntilContextTimeout(waitCtx, 2*time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		updated, err := kubeClient.CertificatesV1().CertificateSigningRequests().Get(ctx, csrName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(updated.Status.Certificate) > 0 {
			signedCert = updated.Status.Certificate
			return true, nil
		}
		return false, nil
	})
	if pollErr != nil {
		return nil, fmt.Errorf("waiting for CSR %s to be signed: %w", csrName, pollErr)
	}

	return &kubeletCertBundle{
		ClientCertPEM: string(signedCert),
		ClientKeyPEM:  string(keyPEM),
	}, nil
}
