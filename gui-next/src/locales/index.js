import { applyPlusLocale } from "../plugins/plusLocale.js";
import zh from "./zh";
import en from "./en";
import fa from "./fa-ir";
import ru from "./ru";
import pt from "./pt-br";
import ko from "./ko";

for (const [key, messages] of Object.entries({ en, zh, fa, ru, pt, ko })) {
  applyPlusLocale(key, messages);
}

export default {
  zh,
  en,
  fa,
  ru,
  pt,
  ko,
};
