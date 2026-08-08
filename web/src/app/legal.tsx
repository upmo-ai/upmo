import { Anchor, Text, Title } from "@mantine/core";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { BrandLink } from "./brand";

import "./sign-in.css";

const updated = "29 October 2026";

type Section = {
  heading: string;
  paragraphs: ReactNode[];
};

function contact(about: string) {
  return (
    <>
      Questions about {about} on upmo.ai: <Anchor href="mailto:security@upmo.ai">security@upmo.ai</Anchor>. For a
      copy your organisation runs, contact the person who runs it.
    </>
  );
}

const terms: Section[] = [
  {
    heading: "These terms",
    paragraphs: [
      "These terms cover use of an Upmo workspace. Upmo runs tasks for the people in that workspace. The workspace may be the managed service at upmo.ai, or a copy that an organisation runs for itself. By logging in, you agree to these terms.",
    ],
  },
  {
    heading: "The workspace",
    paragraphs: [
      "One workspace serves one organisation. The person who runs it chooses who may join, what each person may do, and which outside services it may use.",
    ],
  },
  {
    heading: "Your account",
    paragraphs: [
      "You need an account. You log in with a passkey, with a one-time link that creates a passkey, or with a password if you have set one. Keep your passkey and password to yourself. If you think someone else has used your account, tell the person who runs the workspace.",
    ],
  },
  {
    heading: "Acceptable use",
    paragraphs: [
      "Use the workspace for the organisation's work. Do not break the law with it, harm other people, or try to enter an account or a workspace that is not yours. Do not interfere with the service, or try to get around a limit that protects it.",
    ],
  },
  {
    heading: "Your material",
    paragraphs: [
      "Tasks, chat, files, knowledge and the other material you add belong to the organisation, not to Upmo. Upmo does not claim ownership of it. The workspace stores and processes that material so it can do the work you ask for. When a task or a chat uses a model, or a connector reaches an outside service, the part needed for that work is sent there.",
    ],
  },
  {
    heading: "Who provides the workspace",
    paragraphs: [
      "On upmo.ai, Upmo provides the workspace and may suspend it if these terms are broken or if other people are at risk. If your organisation runs its own copy, the person who runs that copy provides the workspace, and these terms apply between that person and the people who use it.",
    ],
  },
  {
    heading: "Availability",
    paragraphs: [
      "The workspace can be unavailable while it is updated or repaired, and a task can fail. Keep your own copy of anything you cannot afford to lose.",
    ],
  },
  {
    heading: "Stopping",
    paragraphs: [
      "You may stop using the workspace at any time. The person who runs it may disable an account. On upmo.ai, Upmo may close a workspace that breaks these terms. Disabling an account does not by itself delete material the organisation still needs.",
    ],
  },
  {
    heading: "Liability",
    paragraphs: [
      "The workspace is provided as it stands. To the extent the law allows, the person who provides it is not liable for lost work, lost profit, or other loss that was not a direct result of failing to take reasonable care. Nothing in these terms limits liability that the law does not allow to be limited.",
    ],
  },
  {
    heading: "Changes",
    paragraphs: [
      "These terms can change. The date above is the date of this version. If you keep logging in after a change, you accept the new terms. If you do not accept them, stop using the workspace.",
    ],
  },
  {
    heading: "Contact",
    paragraphs: [contact("these terms")],
  },
];

const privacy: Section[] = [
  {
    heading: "This policy",
    paragraphs: [
      "This policy describes the personal information an Upmo workspace keeps so people can use it. The person who runs the workspace decides why that information is kept and is responsible for it. On upmo.ai, that is Upmo. If your organisation runs its own copy, the person who runs that copy is responsible.",
    ],
  },
  {
    heading: "Your account",
    paragraphs: [
      "The workspace keeps your name and email address so it knows who you are and so other people in the workspace can work with you.",
    ],
  },
  {
    heading: "How you log in",
    paragraphs: [
      "A passkey proves it is you. The private key stays on your device or with your passkey provider. The workspace keeps the public credential, a name for the passkey, and when it was added and last used. It does not keep a fingerprint or a picture of your face.",
      "A password is optional. If you set one, the workspace stores a salted hash of it, not the password itself.",
      "A one-time link is kept only as a hash, with an expiry, until it is used or it expires.",
    ],
  },
  {
    heading: "This browser",
    paragraphs: [
      "A cookie keeps you logged in on this browser. Scripts on the page cannot read it. It is cleared when you log out, when it expires, or when the account is disabled. The workspace also keeps when you logged in, whether you used a passkey, a link or a password, and the browser name, so you can see where you are logged in and log out somewhere else.",
      "The network address of a log-in attempt is kept only to slow repeated guesses. It is not a history of the places you have been.",
    ],
  },
  {
    heading: "Your work",
    paragraphs: [
      "The workspace keeps the tasks, chat, files, knowledge, automations and settings you and the other people add. That material can include personal information if you put it there.",
    ],
  },
  {
    heading: "Models and connectors",
    paragraphs: [
      "If the workspace is set up to use a model, the text needed for a task or a chat is sent to that model provider. If it uses a connector, the relevant part of the work is sent to that outside service, such as a mail service or a source control host. That provider's own terms and privacy policy then apply to what they receive. Upmo does not sell personal information, and it does not use it for advertising.",
    ],
  },
  {
    heading: "Who can see it",
    paragraphs: [
      "People in the workspace see what their role and the sharing settings allow. The person who runs the workspace can see the accounts. A model provider or a connector receives information only when the workspace uses it for a piece of work.",
    ],
  },
  {
    heading: "How long it is kept",
    paragraphs: [
      "Account information stays while the account exists. The cookie, and the record of a browser you are logged in on, stay until you log out, the cookie expires, or the account is disabled. A one-time link is removed from use once it is spent or it expires. Material you add stays until someone with permission deletes it, or the workspace is removed.",
    ],
  },
  {
    heading: "Cookies",
    paragraphs: [
      "The only cookie is the one that keeps you logged in. It is not used to follow you on other websites. There is no advertising cookie.",
    ],
  },
  {
    heading: "Your choices",
    paragraphs: [
      "You can ask for your name or email to be corrected, for a copy of the personal information the workspace holds about you, or for your account to be removed. On upmo.ai, email security@upmo.ai. For a copy your organisation runs, ask the person who runs it. You can remove a passkey or a password in the security settings, as long as one way to log in remains. You can log out of another browser from those settings.",
    ],
  },
  {
    heading: "Children",
    paragraphs: ["The workspace is for an organisation's work. It is not directed at children."],
  },
  {
    heading: "Changes",
    paragraphs: ["This policy can change. The date above is the date of this version."],
  },
  {
    heading: "Contact",
    paragraphs: [contact("this policy")],
  },
];

function Document({ title, sections }: { title: string; sections: Section[] }) {
  return (
    <main className="legal">
      <article className="legal-body">
        <BrandLink className="sign-in-wordmark legal-mark" />
        <Title order={1}>{title}</Title>
        <Text className="legal-updated" size="sm">
          Last updated {updated}
        </Text>
        {sections.map((section) => (
          <section key={section.heading} className="legal-section">
            <Title order={2}>{section.heading}</Title>
            {section.paragraphs.map((paragraph, index) => (
              <Text key={index}>{paragraph}</Text>
            ))}
          </section>
        ))}
        <Anchor component={Link} to="/login">
          Back to log in
        </Anchor>
      </article>
    </main>
  );
}

export function TermsPage() {
  return <Document title="Terms of use" sections={terms} />;
}

export function PrivacyPage() {
  return <Document title="Privacy policy" sections={privacy} />;
}
