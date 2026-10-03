import Alert from 'hew/Alert';
import CodeEditor from 'hew/CodeEditor';
import Form from 'hew/Form';
import Input from 'hew/Input';
import { Modal } from 'hew/Modal';
import Select, { Option } from 'hew/Select';
import { useToast } from 'hew/Toast';
import { Loadable } from 'hew/utils/loadable';
import yaml from 'js-yaml';
import { useObservable } from 'micro-observables';
import React, { useCallback, useEffect, useId, useMemo, useState } from 'react';

import { createTaskTemplate, updateTaskTemplate, updateTaskTemplateName } from 'services/api';
import workspaceStore from 'stores/workspaces';
import { RawJson, Template, Workspace } from 'types';
import handleError, { DetError, ErrorLevel, ErrorType } from 'utils/error';
import { sensitiveEnvNames } from 'utils/ntscConfig';

const FORM_ID = 'create-template-form';

interface FormInputs {
  name: string;
  workspaceId: number;
  config: string;
}

interface Props {
  /** Create mode only: YAML to start the new template from. */
  initialConfig?: string;
  /** Create mode only: preselected workspace that the user can still change. */
  initialWorkspaceId?: number;
  workspaceId?: number;
  onSuccess?: () => void;
  template?: Template;
}

const TemplateCreateModalComponent: React.FC<Props> = ({
  initialConfig,
  initialWorkspaceId,
  workspaceId,
  onSuccess,
  template,
}) => {
  const idPrefix = useId();
  const { openToast } = useToast();
  const [form] = Form.useForm<FormInputs>();
  const [disabled, setDisabled] = useState<boolean>(true);
  const workspaces = Loadable.getOrElse([], useObservable(workspaceStore.workspaces));

  const onChange = useCallback(() => {
    const fields = form.getFieldsError();
    const hasError = fields.some((f) => f.errors.length);
    setDisabled(hasError);
  }, [form]);

  const handleSubmit = useCallback(async () => {
    const values = await form.validateFields();

    try {
      if (values) {
        if (template) {
          await updateTaskTemplate({
            config: yaml.load(values.config),
            name: template.name,
            workspaceId: values.workspaceId,
          });

          // Edit template name
          if (template.name !== values.name) {
            await updateTaskTemplateName({
              newName: values.name,
              oldName: template.name,
            });
          }
          openToast({
            description: `Template ${values.name} has been updated`,
            severity: 'Info',
            title: 'Template Updated',
          });
        } else {
          await createTaskTemplate({
            ...values,
            config: yaml.load(values.config),
          });
          openToast({
            description: `Template ${values.name} has been created`,
            severity: 'Info',
            title: 'Template Created',
          });
        }

        form.resetFields();
        onSuccess?.();
      }
    } catch (e) {
      if (e instanceof DetError) {
        handleError(e, {
          level: e.level,
          publicMessage: e.publicMessage,
          publicSubject: 'Unable to manage template.',
          silent: false,
          type: e.type,
        });
      } else {
        handleError(e, {
          level: ErrorLevel.Error,
          publicMessage: 'Please try again later.',
          publicSubject: 'Unable to manage template.',
          silent: false,
          type: ErrorType.Server,
        });
      }
      throw e;
    }
  }, [form, openToast, onSuccess, template]);

  useEffect(() => {
    if (!template || !form) return;
    form.setFieldsValue({
      config: yaml.dump(template.config),
      name: template.name,
      workspaceId: template.workspaceId,
    });
  }, [template, form]);

  // Create mode with a starting config, e.g. "Save as Template" in the launch modal.
  useEffect(() => {
    if (template || initialConfig === undefined) return;
    form.setFieldsValue({ config: initialConfig });
  }, [form, initialConfig, template]);

  const initialSensitiveEnv = useMemo(() => {
    if (template || !initialConfig) return [];
    try {
      const parsed = yaml.load(initialConfig);
      return parsed && typeof parsed === 'object' ? sensitiveEnvNames(parsed as RawJson) : [];
    } catch {
      return [];
    }
  }, [initialConfig, template]);

  return (
    <Modal
      cancel
      size="large"
      submit={{
        disabled,
        form: idPrefix + FORM_ID,
        handleError,
        handler: handleSubmit,
        text: `${template ? 'Edit' : 'Create'} Template`,
      }}
      title={`${template ? 'Edit' : 'New'} Template`}>
      {!template && initialConfig !== undefined && (
        <Alert
          description={
            initialSensitiveEnv.length > 0
              ? `These environment variables look like credentials: ${initialSensitiveEnv.join(', ')}. Remove them unless every user may read them.`
              : undefined
          }
          message="Other users can read templates. Review the config before you save it."
          showIcon
          type={initialSensitiveEnv.length > 0 ? 'warning' : 'info'}
        />
      )}
      <Form
        autoComplete="off"
        form={form}
        id={idPrefix + FORM_ID}
        layout="vertical"
        onFieldsChange={onChange}>
        <Form.Item
          initialValue={template?.name}
          label="Name"
          name="name"
          rules={[{ message: 'Name is required.', required: true }]}>
          <Input />
        </Form.Item>
        <Form.Item
          initialValue={template?.workspaceId ?? workspaceId ?? initialWorkspaceId}
          label="Workspace"
          name="workspaceId"
          rules={[{ message: 'Workspace is required', required: true, type: 'number' }]}>
          <Select
            allowClear
            disabled={!!workspaceId && !template}
            placeholder="Workspace (required)">
            {workspaces.map((workspace: Workspace) => (
              <Option key={workspace.id} value={workspace.id}>
                {workspace.name}
              </Option>
            ))}
          </Select>
        </Form.Item>
        <Form.Item
          label="Config"
          name="config"
          rules={[
            { message: 'Template content is required', required: true },
            {
              validator: (_rule, value) => {
                try {
                  yaml.load(value);
                  return Promise.resolve();
                } catch (err: unknown) {
                  return Promise.reject(
                    new Error(
                      `Invalid YAML on line ${(err as { mark: { line: string } }).mark.line}.`,
                    ),
                  );
                }
              },
            },
          ]}>
          <CodeEditor
            file={template?.config ? yaml.dump(template.config) : initialConfig ?? ''}
            files={[{ key: 'template.yaml' }]}
            height="40vh"
            onError={handleError}
          />
        </Form.Item>
        {form.getFieldError('config').length > 0 && (
          <Alert message={form.getFieldError('config').join('\n')} type="error" />
        )}
      </Form>
    </Modal>
  );
};

export default TemplateCreateModalComponent;
