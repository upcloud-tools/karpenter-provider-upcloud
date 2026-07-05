package v1alpha1

import (
	"github.com/UpCloudLtd/upcloud-go-api/v8/upcloud"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

func (in *UpCloudNodeClass) DeepCopyInto(out *UpCloudNodeClass) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *UpCloudNodeClass) DeepCopy() *UpCloudNodeClass {
	if in == nil {
		return nil
	}
	out := new(UpCloudNodeClass)
	in.DeepCopyInto(out)
	return out
}

func (in *UpCloudNodeClass) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *UpCloudNodeClassList) DeepCopyInto(out *UpCloudNodeClassList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]UpCloudNodeClass, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *UpCloudNodeClassList) DeepCopy() *UpCloudNodeClassList {
	if in == nil {
		return nil
	}
	out := new(UpCloudNodeClassList)
	in.DeepCopyInto(out)
	return out
}

func (in *UpCloudNodeClassList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *UpCloudNodeClassSpec) DeepCopyInto(out *UpCloudNodeClassSpec) {
	*out = *in
	if in.SSHKeys != nil {
		in, out := &in.SSHKeys, &out.SSHKeys
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.KubeletArgs != nil {
		in, out := &in.KubeletArgs, &out.KubeletArgs
			*out = make([]upcloud.KubernetesKubeletArg, len(*in))
			copy(*out, *in)
		}
		if in.Labels != nil {
			in, out := &in.Labels, &out.Labels
			*out = make(map[string]string, len(*in))
			for key, val := range *in {
				(*out)[key] = val
			}
		}
		if in.Taints != nil {
			in, out := &in.Taints, &out.Taints
			*out = make([]upcloud.KubernetesTaint, len(*in))
		copy(*out, *in)
	}
	if in.AntiAffinity != nil {
		in, out := &in.AntiAffinity, &out.AntiAffinity
		*out = new(bool)
		**out = **in
	}
	if in.UtilityNetworkAccess != nil {
		in, out := &in.UtilityNetworkAccess, &out.UtilityNetworkAccess
		*out = new(bool)
		**out = **in
	}
}

func (in *UpCloudNodeClassSpec) DeepCopy() *UpCloudNodeClassSpec {
	if in == nil {
		return nil
	}
	out := new(UpCloudNodeClassSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *UpCloudNodeClassStatus) DeepCopyInto(out *UpCloudNodeClassStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]v1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *UpCloudNodeClassStatus) DeepCopy() *UpCloudNodeClassStatus {
	if in == nil {
		return nil
	}
	out := new(UpCloudNodeClassStatus)
	in.DeepCopyInto(out)
	return out
}
