import { Button, Form, Modal } from "antd";
import type { FormInstance, FormProps, ModalProps } from "antd";
import { useRef, useState, type ReactNode } from "react";

import { FormActions } from "./FormActions";

export interface FormModalProps<Values = Record<string, unknown>>
  extends Omit<ModalProps, "afterOpenChange" | "destroyOnHidden" | "footer" | "onCancel" | "onOk"> {
  form: FormInstance<Values>;
  formProps?: Omit<FormProps<Values>, "form" | "onFinish">;
  onOpenChange: (open: boolean) => void;
  /** Resolve true only after a successful save; false or rejection keeps the draft open. */
  onSubmit: (values: Values) => Promise<boolean>;
  preserveOnClose?: boolean;
  submitText?: ReactNode;
  cancelText?: ReactNode;
  children: ReactNode;
}

/** Form-specific Modal lifecycle; feedback and mutation details remain owned by callers. */
export function FormModal<Values = Record<string, unknown>>({
  form,
  formProps,
  open,
  onOpenChange,
  onSubmit,
  preserveOnClose = false,
  submitText = "确定",
  cancelText = "取消",
  children,
  ...modalProps
}: FormModalProps<Values>) {
  const [pending, setPending] = useState(false);
  const pendingRef = useRef(false);

  async function submit(values: Values): Promise<void> {
    if (pendingRef.current) return;
    pendingRef.current = true;
    setPending(true);
    try {
      const succeeded = await onSubmit(values);
      if (succeeded) {
        form.resetFields();
        onOpenChange(false);
      }
    } catch {
      // The caller owns controlled error feedback. A rejected submit keeps the draft open.
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  }

  function close(): void {
    if (pendingRef.current) return;
    onOpenChange(false);
  }

  return (
    <Modal
      {...modalProps}
      open={open}
      footer={null}
      closable={!pending}
      maskClosable={!pending}
      destroyOnHidden={!preserveOnClose}
      onCancel={close}
      afterOpenChange={(nextOpen) => {
        if (!nextOpen && !preserveOnClose) form.resetFields();
      }}
    >
      <Form<Values> {...formProps} form={form} onFinish={submit}>
        {children}
        <FormActions className="form-modal__actions">
          <Button disabled={pending} onClick={close}>
            {cancelText}
          </Button>
          <Button type="primary" htmlType="submit" loading={pending}>
            {submitText}
          </Button>
        </FormActions>
      </Form>
    </Modal>
  );
}
